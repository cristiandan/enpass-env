package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cristiandan/enpass-env/internal/enpass"
	"github.com/cristiandan/enpass-env/internal/unlock"

	"github.com/sirupsen/logrus"
	"golang.org/x/term"
)

// defaultVault is $ENPASS_VAULT, else the first primary vault found in the
// places Enpass for macOS keeps it.
func defaultVault() string {
	if v := os.Getenv("ENPASS_VAULT"); v != "" {
		return v
	}
	home := os.Getenv("HOME")
	candidates := []string{
		filepath.Join(home, "Library/Containers/in.sinew.Enpass-Desktop/Data/Documents/Vaults/primary"),
		filepath.Join(home, "Documents/Enpass/Vaults/primary"),
	}
	for _, c := range candidates {
		if _, err := os.Stat(filepath.Join(c, "vault.enpassdb")); err == nil {
			return c
		}
	}
	return candidates[0]
}

type opts struct {
	vault   string
	db      string
	keyfile string
	debug   bool
}

func main() {
	var o opts
	flag.StringVar(&o.vault, "vault", defaultVault(), "Enpass vault directory (or set ENPASS_VAULT)")
	flag.StringVar(&o.db, "db", "", "read a different database file (opened from a temporary copy, read-only)")
	flag.StringVar(&o.keyfile, "keyfile", "", "vault keyfile, if the vault uses one")
	flag.BoolVar(&o.debug, "debug", false, "debug logging")
	flag.Usage = usage

	// Installed as envpush / envpull / envstatus symlinks: the name is the command.
	switch name := filepath.Base(os.Args[0]); name {
	case "envpush", "envpull", "envstatus":
		os.Args = append([]string{os.Args[0], strings.TrimPrefix(name, "env")}, os.Args[1:]...)
	}
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}

	var err error
	switch cmd, rest := args[0], args[1:]; cmd {
	case "push", "pull", "status", "list":
		if cmd == "push" && o.db != "" {
			err = fmt.Errorf("push can't be used with -db")
			break
		}
		err = map[string]func(opts, []string) error{
			"push": cmdPush, "pull": cmdPull, "status": cmdStatus, "list": cmdList,
		}[cmd](o, rest)
	case "unlock":
		err = withVault(o, func(v *enpass.Vault) error {
			fmt.Fprintln(os.Stderr, "vault opened; key stored in Keychain behind Touch ID")
			return nil
		})
	case "forget":
		err = forget(o)
	case "probe":
		err = probe(o, rest)
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: enpass-env [flags] <command> [-f FILE] [-project NAME] [-force]

Keeps env files in sync through Enpass: one Secure Note per file, titled
env:<project> (or env:<project>/<file> for files other than .env).

commands:
  push      store ./.env in Enpass (refuses if Enpass has newer changes)
  pull      write ./.env from Enpass (refuses if local has unpushed changes)
  status    compare ./.env with Enpass, by variable name
  list      list env:* notes in Enpass
  unlock    open the vault once with the master password; the derived key is
            then kept in Keychain behind Touch ID
  forget    remove the stored key from Keychain
  probe     read-only diagnostics: schema | inspect TITLE | find PREFIX

command options:
  -f FILE         env file (default .env)
  -project NAME   project name. Default: "project:" in the file's enpass-env
                  block (written by push/pull), else git repo or folder name
  -force          push/pull over a conflict (pull saves the local file to FILE.bak)

The block of "#" lines between ">>> enpass-env" and "<<< enpass-env" at the top
of the file records what was last synced. It is never stored in Enpass.

flags:
`)
	flag.PrintDefaults()
}

// backupVault copies the vault database aside before a write, keeping the
// most recent backups.
func backupVault(o opts) error {
	const keep = 20
	dir := filepath.Join(os.Getenv("HOME"), ".local/share/enpass-env/backups")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	dst := filepath.Join(dir, "vault-"+time.Now().Format("20060102-150405")+".enpassdb")
	if err := copyFile(filepath.Join(o.vault, "vault.enpassdb"), dst); err != nil {
		return fmt.Errorf("backing up vault: %w", err)
	}
	old, _ := filepath.Glob(filepath.Join(dir, "vault-*.enpassdb"))
	sort.Strings(old)
	for len(old) > keep {
		os.Remove(old[0])
		old = old[1:]
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func logLevel(o opts) logrus.Level {
	if o.debug {
		return logrus.DebugLevel
	}
	return logrus.WarnLevel
}

// withVault opens the vault, unlocking via Keychain/Touch ID when a key is
// stored, otherwise prompting for the master password and storing the key.
func withVault(o opts, fn func(*enpass.Vault) error) error {
	vaultPath, err := filepath.EvalSymlinks(o.vault)
	if err != nil {
		return err
	}
	v, err := enpass.NewVault(vaultPath, logLevel(o))
	if err != nil {
		return err
	}

	if o.db != "" {
		tmp, err := copyToTemp(o.db)
		if err != nil {
			return err
		}
		defer os.RemoveAll(filepath.Dir(tmp))
		v.SetDatabaseFile(tmp)
	}

	store, err := unlock.NewBiometricStore(vaultPath, logLevel(o))
	if err != nil {
		return err
	}

	creds := &enpass.VaultCredentials{KeyfilePath: o.keyfile}
	if creds.DBKey, err = store.Read(); err != nil {
		return fmt.Errorf("keychain: %w", err)
	}
	if creds.DBKey == nil {
		if creds.Password, err = readPassword("Enpass master password: "); err != nil {
			return err
		}
	}

	if err := v.Open(creds); err != nil {
		return err
	}
	defer v.Close()

	if err := fn(v); err != nil {
		return err
	}
	return store.Write(creds.DBKey)
}

func forget(o opts) error {
	vaultPath, err := filepath.EvalSymlinks(o.vault)
	if err != nil {
		return err
	}
	store, err := unlock.NewBiometricStore(vaultPath, logLevel(o))
	if err != nil {
		return err
	}
	return store.Clean()
}

func readPassword(prompt string) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", fmt.Errorf("no stored key and stdin is not a terminal; run `enpass-env unlock` in a terminal first")
	}
	fmt.Fprint(os.Stderr, prompt)
	pw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	return string(pw), err
}

func copyToTemp(src string) (string, error) {
	dir, err := os.MkdirTemp("", "enpass-env-")
	if err != nil {
		return "", err
	}
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	dst := filepath.Join(dir, filepath.Base(src))
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return dst, err
}
