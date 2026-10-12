package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cristiandan/enpass-env/internal/enpass"
)

type syncArgs struct {
	file    string // env file path, relative to cwd
	project string
	force   bool
}

func parseSyncArgs(cmd string, args []string) (syncArgs, error) {
	var a syncArgs
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.StringVar(&a.file, "f", ".env", "env file")
	fs.StringVar(&a.project, "project", "", "project name (default: the file's state block, else git repo / folder name)")
	fs.BoolVar(&a.force, "force", false, "overwrite despite a conflict")
	if err := fs.Parse(args); err != nil {
		return a, err
	}
	if fs.NArg() > 0 {
		return a, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	return a, nil
}

// syncCtx is everything known about one env file before deciding what to do.
type syncCtx struct {
	path, file, project, title string

	raw      string // the file as on disk, state block included
	local    string // the synced content: raw minus the state block
	hasLocal bool
	state    *syncState // from the file's state block; nil if unknown
	legacy   bool       // has a .env.enpass file or project line to migrate
	note     *envNote
}

type syncStatus int

const (
	upToDate    syncStatus = iota
	noRemote               // nothing in Enpass yet
	noLocal                // in Enpass, no local file
	localAhead             // changed here since last sync
	remoteAhead            // changed in Enpass since last sync
	conflict               // both changed, or no shared history and they differ
)

func newSyncCtx(a syncArgs) (*syncCtx, error) {
	c := &syncCtx{path: a.file, file: filepath.Base(a.file)}
	var err error
	if c.raw, c.hasLocal, err = readEnv(c.path); err != nil {
		return nil, err
	}
	c.state, c.local = splitBlock(c.raw)

	// Migrate from the .env.enpass file and "# enpass-env: project=" line.
	legacyProject, rest := stripLegacyProject(c.local)
	if legacyProject != "" {
		c.local, c.legacy = rest, true
	}
	if c.state == nil {
		if c.state = loadLegacyState(c.path); c.state != nil {
			c.legacy = true
		}
	}

	switch {
	case a.project != "":
		c.project = a.project
	case c.state != nil && c.state.Project != "":
		c.project = c.state.Project
	case legacyProject != "":
		c.project = legacyProject
	default:
		c.project = defaultProject(filepath.Dir(c.path))
	}
	if strings.ContainsAny(c.project, "/\n") || c.project == "" {
		return nil, fmt.Errorf("invalid project name %q", c.project)
	}
	c.title = noteTitle(c.project, c.file)
	// A block recorded for a different note says nothing about this one.
	if c.state != nil && c.state.Title != c.title {
		c.state = nil
	}
	return c, nil
}

func (c *syncCtx) status() syncStatus {
	if c.note == nil {
		return noRemote
	}
	if !c.hasLocal {
		return noLocal
	}
	localSHA, remoteSHA := sha(c.local), sha(c.note.Body)
	if localSHA == remoteSHA {
		return upToDate
	}
	if c.state == nil {
		return conflict
	}
	localChanged, remoteChanged := localSHA != c.state.SHA256, remoteSHA != c.state.SHA256
	switch {
	case localChanged && remoteChanged:
		return conflict
	case localChanged:
		return localAhead
	default:
		return remoteAhead
	}
}

func (c *syncCtx) remoteLabel() string {
	n := c.note
	s := fmt.Sprintf("v%d, pushed %s by %s", n.Version, ago(n.UpdatedAt), n.UpdatedBy)
	if n.EditedInApp() {
		s += fmt.Sprintf("; edited in Enpass %s", ago(n.Modified))
	}
	return s
}

// writeLocal writes content to the env file under a state block describing
// the note it now matches, and drops any legacy state file.
func (c *syncCtx) writeLocal(content string) error {
	c.state = &syncState{
		Project:  c.project,
		Title:    c.title,
		Version:  c.note.Version,
		SHA256:   sha(c.note.Body),
		SyncedAt: time.Now(),
	}
	mode := os.FileMode(0o600)
	if c.hasLocal {
		mode = fileMode(c.path)
	}
	if err := writeFileAtomic(c.path, []byte(renderBlock(c.state)+content), mode); err != nil {
		return err
	}
	c.local, c.hasLocal = content, true
	if err := os.Remove(legacyStatePath(c.path)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// recordInSync updates the state block after finding the file already
// matches the note, unless it already says so.
func (c *syncCtx) recordInSync() error {
	if !c.legacy && c.state != nil && c.state.Version == c.note.Version && c.state.SHA256 == sha(c.note.Body) {
		return nil
	}
	return c.writeLocal(c.local)
}

func (c *syncCtx) warnings() {
	if dups := duplicateKeys(c.local); len(dups) > 0 {
		fmt.Fprintf(os.Stderr, "warning: %s sets %s more than once; the last one wins\n", c.file, strings.Join(dups, ", "))
	}
	if exposed := gitUnignored(c.path, c.path+".bak"); len(exposed) > 0 {
		fmt.Fprintf(os.Stderr, "warning: not in .gitignore: %s\n", strings.Join(exposed, ", "))
	}
}

func cmdStatus(o opts, args []string) error {
	a, err := parseSyncArgs("status", args)
	if err != nil {
		return err
	}
	c, err := newSyncCtx(a)
	if err != nil {
		return err
	}
	return withVault(o, func(v *enpass.Vault) error {
		if c.note, err = findNote(v.DB(), c.title); err != nil {
			return err
		}
		fmt.Printf("%s  <->  Enpass %q\n", c.path, c.title)
		printSyncAge(o)
		switch c.status() {
		case upToDate:
			fmt.Printf("up to date (%s)\n", c.remoteLabel())
		case noRemote:
			fmt.Println("not in Enpass yet; run push")
		case noLocal:
			fmt.Printf("no local %s; run pull (%s)\n", c.file, c.remoteLabel())
		case localAhead:
			fmt.Printf("changed here since last sync; run push\n%s", diffEnv(c.note.Body, c.local))
		case remoteAhead:
			fmt.Printf("Enpass has a newer version (%s); run pull\n%s", c.remoteLabel(), diffEnv(c.local, c.note.Body))
		case conflict:
			fmt.Printf("CONFLICT: changed both here and in Enpass (%s)\npulling would change:\n%s", c.remoteLabel(), diffEnv(c.local, c.note.Body))
		}
		if c.hasLocal {
			c.warnings()
		}
		return nil
	})
}

func cmdPull(o opts, args []string) error {
	a, err := parseSyncArgs("pull", args)
	if err != nil {
		return err
	}
	c, err := newSyncCtx(a)
	if err != nil {
		return err
	}
	return withVault(o, func(v *enpass.Vault) error {
		if c.note, err = findNote(v.DB(), c.title); err != nil {
			return err
		}
		printSyncAge(o)
		st := c.status()
		switch st {
		case noRemote:
			return fmt.Errorf("no %q in Enpass on this machine.\n"+
				"If it was pushed elsewhere, Enpass here may not have synced it yet (last sync: %s):\n"+
				"open Enpass and let it sync, then retry. `enpass-env list` shows what's here",
				c.title, ago(lastSync(o)))
		case upToDate:
			fmt.Printf("%s is up to date (%s)\n", c.path, c.remoteLabel())
			return c.recordInSync()
		case localAhead:
			if !a.force {
				return fmt.Errorf("%s has changes not in Enpass; push them, or pull -force to discard:\n%s", c.path, diffEnv(c.note.Body, c.local))
			}
		case conflict:
			if !a.force {
				return fmt.Errorf("conflict: %s and Enpass both changed (%s). pulling would change:\n%s"+
					"push -force keeps local, pull -force keeps Enpass (local is saved to %s.bak)",
					c.path, c.remoteLabel(), diffEnv(c.local, c.note.Body), c.file)
			}
		}

		had, prev := c.hasLocal, c.local
		if had {
			if err := writeFileAtomic(c.path+".bak", []byte(c.raw), 0o600); err != nil {
				return fmt.Errorf("backing up %s: %w", c.path, err)
			}
		}
		if err := c.writeLocal(c.note.Body); err != nil {
			return err
		}
		fmt.Printf("pulled %s (%s)\n", c.path, c.remoteLabel())
		if had {
			fmt.Print(diffEnv(prev, c.note.Body))
			fmt.Printf("previous version saved to %s.bak\n", c.path)
		}
		c.warnings()
		return nil
	})
}

func cmdPush(o opts, args []string) error {
	a, err := parseSyncArgs("push", args)
	if err != nil {
		return err
	}
	c, err := newSyncCtx(a)
	if err != nil {
		return err
	}
	if !c.hasLocal {
		return fmt.Errorf("no %s here", c.path)
	}
	return withVault(o, func(v *enpass.Vault) error {
		db := v.DB()
		if c.note, err = findNote(db, c.title); err != nil {
			return err
		}
		st := c.status()
		switch st {
		case upToDate:
			fmt.Printf("Enpass already has this %s (%s)\n", c.file, c.remoteLabel())
			c.warnings()
			return c.recordInSync()
		case remoteAhead:
			if !a.force {
				return fmt.Errorf("Enpass has a newer version (%s); pull first, or push -force to overwrite it. Pulling would change:\n%s", c.remoteLabel(), diffEnv(c.local, c.note.Body))
			}
		case conflict:
			if !a.force {
				return fmt.Errorf("conflict: %s and Enpass both changed (%s). pulling would change:\n%s"+
					"push -force keeps local, pull -force keeps Enpass", c.path, c.remoteLabel(), diffEnv(c.local, c.note.Body))
			}
		}

		host, _ := os.Hostname()
		next := &envNote{
			Project:   c.project,
			File:      c.file,
			Version:   1,
			UpdatedAt: time.Now(),
			UpdatedBy: strings.TrimSuffix(host, ".local"),
			Repo:      gitRemote(filepath.Dir(c.path)),
			SHA256:    sha(c.local),
			Body:      c.local,
		}
		if c.note != nil {
			next.Version = c.note.Version + 1
		}

		if err := checkWritable(db, v.Info().VaultVersion); err != nil {
			return err
		}
		if err := backupVault(o); err != nil {
			return err
		}
		if c.note == nil {
			err = insertNote(db, c.title, next.render())
		} else {
			err = updateNote(db, c.note.UUID, next.render())
		}
		if err != nil {
			return err
		}

		prev := c.note
		c.note = next
		if err := c.writeLocal(c.local); err != nil {
			return fmt.Errorf("pushed, but updating %s's state block failed: %w", c.file, err)
		}
		if prev == nil {
			fmt.Printf("created %q v1 with %d variables\n", c.title, len(envKeys(c.local)))
		} else {
			fmt.Printf("updated %q to v%d\n%s", c.title, next.Version, diffEnv(prev.Body, c.local))
		}
		fmt.Println("Enpass uploads it on its next sync, which this doesn't trigger:\n" +
			"make any edit in Enpass so your other machines get it.")
		c.warnings()
		return nil
	})
}

func cmdList(o opts, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("list takes no arguments")
	}
	return withVault(o, func(v *enpass.Vault) error {
		notes, err := listNotes(v.DB())
		if err != nil {
			return err
		}
		if len(notes) == 0 {
			fmt.Println("no env:* notes in Enpass")
			return nil
		}
		sort.Slice(notes, func(i, j int) bool { return notes[i].Title < notes[j].Title })
		for _, n := range notes {
			fmt.Printf("%-40s %-40s %s\n", n.Title, strings.TrimPrefix(n.Subtitle, "enpass-env · "), ago(n.Modified))
		}
		return nil
	})
}

// printSyncAge says when Enpass on this machine last exchanged data with the
// cloud, since pull and status only see what has synced down so far.
func printSyncAge(o opts) {
	if t := lastSync(o); !t.IsZero() {
		fmt.Printf("(Enpass last synced with the cloud %s)\n", ago(t))
	}
}

// lastSync is when Enpass last wrote its local copy of the cloud vault.
func lastSync(o opts) time.Time {
	syncDir := filepath.Join(filepath.Dir(filepath.Dir(o.vault)), ".sync", filepath.Base(o.vault))
	matches, _ := filepath.Glob(filepath.Join(syncDir, "*.enpassdb"))
	var newest time.Time
	for _, m := range matches {
		if filepath.Base(m) == "vault.enpassdb" {
			continue
		}
		if fi, err := os.Stat(m); err == nil && fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
	}
	return newest
}

func fileMode(path string) os.FileMode {
	if fi, err := os.Stat(path); err == nil {
		return fi.Mode().Perm()
	}
	return 0o600
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "at an unknown time"
	}
	d := time.Since(t).Round(time.Minute)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return t.Local().Format("2006-01-02 15:04")
	}
}
