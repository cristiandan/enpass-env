package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// syncState is what this machine last pulled or pushed. It lives in a comment
// block at the top of the env file (see renderBlock) and holds no secret values.
type syncState struct {
	Project  string
	Title    string
	Version  int
	SHA256   string
	SyncedAt time.Time
}

const (
	blockBegin = "# >>> enpass-env"
	blockEnd   = "# <<< enpass-env"
)

// renderBlock formats the state block. It ends with a newline and is followed
// directly by the synced content.
func renderBlock(s *syncState) string {
	var b strings.Builder
	b.WriteString(blockBegin + " (managed by envpush/envpull; edit project to sync with another note)\n")
	fmt.Fprintf(&b, "# project: %s\n", s.Project)
	if s.Title != "" {
		fmt.Fprintf(&b, "# note: %s\n", s.Title)
		fmt.Fprintf(&b, "# version: %d\n", s.Version)
		fmt.Fprintf(&b, "# sha256: %s\n", s.SHA256)
		fmt.Fprintf(&b, "# synced_at: %s\n", s.SyncedAt.Local().Format(time.RFC3339))
	}
	b.WriteString(blockEnd + "\n")
	return b.String()
}

// splitBlock separates the state block from the synced content. It returns a
// nil state if the file has no block.
func splitBlock(file string) (*syncState, string) {
	lines := strings.SplitAfter(file, "\n")
	begin, end := -1, -1
	for i, l := range lines {
		t := strings.TrimRight(l, "\r\n")
		if begin < 0 && strings.HasPrefix(t, blockBegin) {
			begin = i
		} else if begin >= 0 && t == blockEnd {
			end = i
			break
		}
	}
	if begin < 0 || end < 0 {
		return nil, file
	}
	s := &syncState{}
	for _, l := range lines[begin+1 : end] {
		k, v, _ := strings.Cut(strings.TrimPrefix(strings.TrimSpace(l), "#"), ":")
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "project":
			s.Project = v
		case "note":
			s.Title = v
		case "version":
			s.Version, _ = strconv.Atoi(v)
		case "sha256":
			s.SHA256 = v
		case "synced_at":
			s.SyncedAt, _ = time.Parse(time.RFC3339, v)
		}
	}
	return s, strings.Join(lines[:begin], "") + strings.Join(lines[end+1:], "")
}

// legacyStatePath is where versions before the in-file block kept state.
func legacyStatePath(envPath string) string { return envPath + ".enpass" }

func loadLegacyState(envPath string) *syncState {
	b, err := os.ReadFile(legacyStatePath(envPath))
	if err != nil {
		return nil
	}
	var s struct {
		Project  string    `json:"project"`
		Title    string    `json:"title"`
		Version  int       `json:"version"`
		SHA256   string    `json:"sha256"`
		SyncedAt time.Time `json:"synced_at"`
	}
	if json.Unmarshal(b, &s) != nil {
		return nil
	}
	return &syncState{s.Project, s.Title, s.Version, s.SHA256, s.SyncedAt}
}

// readEnv returns the file contents, or ok=false if it doesn't exist.
func readEnv(path string) (body string, ok bool, err error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	return string(b), err == nil, err
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// defaultProject names the project after the git repository root, falling
// back to the current directory.
func defaultProject(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err == nil {
		return filepath.Base(strings.TrimSpace(string(out)))
	}
	abs, _ := filepath.Abs(dir)
	return filepath.Base(abs)
}

// gitUnignored reports which of the given paths git would commit: inside a
// work tree and not ignored.
func gitUnignored(paths ...string) []string {
	var out []string
	for _, p := range paths {
		dir := filepath.Dir(p)
		if exec.Command("git", "-C", dir, "rev-parse", "--is-inside-work-tree").Run() != nil {
			continue
		}
		if exec.Command("git", "-C", dir, "check-ignore", "-q", filepath.Base(p)).Run() != nil {
			out = append(out, filepath.Base(p))
		}
	}
	return out
}

var envLine = regexp.MustCompile(`^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_.]*)\s*=\s*(.*)$`)

// parseEnv maps variable names to their raw value text. It's used only to
// describe changes; files are always copied verbatim. A later duplicate wins,
// as it does when the file is loaded.
func parseEnv(body string) map[string]string {
	vars := map[string]string{}
	for _, line := range strings.Split(body, "\n") {
		if m := envLine.FindStringSubmatch(line); m != nil {
			vars[m[1]] = strings.TrimSpace(m[2])
		}
	}
	return vars
}

func envKeys(body string) []string {
	vars := parseEnv(body)
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// duplicateKeys lists names assigned more than once.
func duplicateKeys(body string) []string {
	seen := map[string]int{}
	for _, line := range strings.Split(body, "\n") {
		if m := envLine.FindStringSubmatch(line); m != nil {
			seen[m[1]]++
		}
	}
	var dups []string
	for k, n := range seen {
		if n > 1 {
			dups = append(dups, k)
		}
	}
	sort.Strings(dups)
	return dups
}

type keyDiff struct{ Added, Removed, Changed []string }

func (d keyDiff) Empty() bool { return len(d.Added)+len(d.Removed)+len(d.Changed) == 0 }

// diffEnv describes going from old to new, by variable name only.
func diffEnv(old, new string) keyDiff {
	a, b := parseEnv(old), parseEnv(new)
	var d keyDiff
	for k, v := range b {
		if ov, ok := a[k]; !ok {
			d.Added = append(d.Added, k)
		} else if ov != v {
			d.Changed = append(d.Changed, k)
		}
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			d.Removed = append(d.Removed, k)
		}
	}
	sort.Strings(d.Added)
	sort.Strings(d.Removed)
	sort.Strings(d.Changed)
	return d
}

func (d keyDiff) String() string {
	if d.Empty() {
		return "  (no variable changes; only comments/formatting differ)\n"
	}
	var b strings.Builder
	for _, k := range d.Added {
		fmt.Fprintf(&b, "  + %s\n", k)
	}
	for _, k := range d.Removed {
		fmt.Fprintf(&b, "  - %s\n", k)
	}
	for _, k := range d.Changed {
		fmt.Fprintf(&b, "  ~ %s\n", k)
	}
	return b.String()
}

// legacyProjectRe matches the "# enpass-env: project=NAME" line that versions
// before the state block added to env files.
var legacyProjectRe = regexp.MustCompile(`(?m)^#\s*enpass-env:\s*project\s*=\s*(\S+)\s*$\n?`)

// stripLegacyProject removes that line, returning the project it named.
func stripLegacyProject(body string) (project, rest string) {
	m := legacyProjectRe.FindStringSubmatchIndex(body)
	if m == nil {
		return "", body
	}
	return body[m[2]:m[3]], body[:m[0]] + body[m[1]:]
}

// gitRemote returns the origin URL of the repo containing dir, if any.
func gitRemote(dir string) string {
	out, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
