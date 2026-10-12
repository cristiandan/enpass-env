package main

// Enpass storage: one Secure Note per env file. The note is a small header
// (no secret values) followed by the env file verbatim:
//
//	enpass-env · myproject · v3        <- first line, shown as the list preview
//	format: 1
//	version: 3
//	updated_at: 2026-10-12T15:50:00Z
//	updated_by: my-laptop
//	file: .env
//	repo: https://github.com/me/myproject   <- optional
//	sha256: 3f2a…
//	keys: FOO, BAR
//	--- env ---
//	FOO=1
//	BAR=2
//
// Rows are written with every column set exactly as the Enpass app (6.12)
// sets them for a new note; partially filled rows crash the app.

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	noteFormat = 1
	bodyMarker = "--- env ---"
	titlePfx   = "env:"
)

type envNote struct {
	Project   string
	File      string
	Version   int
	UpdatedAt time.Time
	UpdatedBy string
	Repo      string // git origin URL, informational
	SHA256    string // as recorded in the header
	Body      string

	// Set when reading from the vault.
	UUID     string
	Modified time.Time // item.field_updated_at, also bumped by edits in the app
}

// EditedInApp reports whether the body was changed in Enpass after the last
// push, i.e. it no longer matches the hash recorded in the header.
func (n *envNote) EditedInApp() bool { return n.SHA256 != sha(n.Body) }

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func noteTitle(project, file string) string {
	if file == ".env" {
		return titlePfx + project
	}
	return titlePfx + project + "/" + file
}

func (n *envNote) render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "enpass-env · %s · v%d\n", n.Project, n.Version)
	fmt.Fprintf(&b, "format: %d\n", noteFormat)
	fmt.Fprintf(&b, "version: %d\n", n.Version)
	fmt.Fprintf(&b, "updated_at: %s\n", n.UpdatedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "updated_by: %s\n", n.UpdatedBy)
	fmt.Fprintf(&b, "file: %s\n", n.File)
	if n.Repo != "" {
		fmt.Fprintf(&b, "repo: %s\n", n.Repo)
	}
	fmt.Fprintf(&b, "sha256: %s\n", n.SHA256)
	fmt.Fprintf(&b, "keys: %s\n", strings.Join(envKeys(n.Body), ", "))
	b.WriteString(bodyMarker + "\n")
	b.WriteString(n.Body)
	return b.String()
}

func parseNote(text string) (*envNote, error) {
	head, body, ok := strings.Cut(text, "\n"+bodyMarker+"\n")
	if !ok {
		// The app may drop the trailing newline after the marker on an empty body.
		if h, found := strings.CutSuffix(text, "\n"+bodyMarker); found {
			head, body, ok = h, "", true
		}
	}
	if !ok {
		return nil, fmt.Errorf("note has no %q line; not created by enpass-env?", bodyMarker)
	}
	n := &envNote{Body: body}
	for _, line := range strings.Split(head, "\n")[1:] {
		k, v, _ := strings.Cut(line, ":")
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "format":
			if f, _ := strconv.Atoi(v); f > noteFormat {
				return nil, fmt.Errorf("note uses format %d; upgrade enpass-env", f)
			}
		case "version":
			n.Version, _ = strconv.Atoi(v)
		case "updated_at":
			n.UpdatedAt, _ = time.Parse(time.RFC3339, v)
		case "updated_by":
			n.UpdatedBy = v
		case "file":
			n.File = v
		case "repo":
			n.Repo = v
		case "sha256":
			n.SHA256 = v
		}
	}
	return n, nil
}

// findNote returns the live note with the given title, or nil if none exists.
func findNote(db *sql.DB, title string) (*envNote, error) {
	rows, err := db.Query(`
		SELECT uuid, note, field_updated_at FROM item
		WHERE title = ? AND category = 'note' AND trashed = 0 AND deleted = 0`, title)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var found []*envNote
	for rows.Next() {
		var id, text string
		var modified int64
		if err := rows.Scan(&id, &text, &modified); err != nil {
			return nil, err
		}
		n, err := parseNote(text)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", title, err)
		}
		n.UUID, n.Modified = id, time.Unix(modified, 0)
		found = append(found, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	switch len(found) {
	case 0:
		return nil, nil
	case 1:
		return found[0], nil
	default:
		return nil, fmt.Errorf("%d notes titled %q in Enpass; delete the extras", len(found), title)
	}
}

type noteSummary struct {
	Title    string
	Subtitle string
	Modified time.Time
}

func listNotes(db *sql.DB) ([]noteSummary, error) {
	rows, err := db.Query(`
		SELECT title, subtitle, field_updated_at FROM item
		WHERE title LIKE 'env:%' AND category = 'note' AND trashed = 0 AND deleted = 0
		ORDER BY title`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []noteSummary
	for rows.Next() {
		var s noteSummary
		var modified int64
		if err := rows.Scan(&s.Title, &s.Subtitle, &modified); err != nil {
			return nil, err
		}
		s.Modified = time.Unix(modified, 0)
		out = append(out, s)
	}
	return out, rows.Err()
}

// insertNote creates a Secure Note shaped exactly like one made in the app.
func insertNote(db *sql.DB, title, text string) error {
	key := make([]byte, 44) // per-item key: 32-byte AES key + 12-byte nonce
	if _, err := rand.Read(key); err != nil {
		return err
	}
	now := time.Now().Unix()
	subtitle, _, _ := strings.Cut(text, "\n")
	_, err := db.Exec(`
		INSERT INTO item (
			uuid, created_at, meta_updated_at, field_updated_at, title, subtitle, note,
			icon, favorite, trashed, archived, deleted, auto_submit, form_data, category,
			template, wearable, usage_count, last_used, key, extra, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, 0, 0, 0, 1, '', 'note', 'note.default', 0, 0, 0, ?, '', ?)`,
		uuid.New().String(), now, now, now, title, subtitle, text,
		`{"fav":"","image":{"file":"misc/secure_note"},"type":1,"uuid":""}`,
		key, now)
	return err
}

// updateNote rewrites a note the way the app does on edit.
func updateNote(db *sql.DB, id, text string) error {
	now := time.Now().Unix()
	subtitle, _, _ := strings.Cut(text, "\n")
	res, err := db.Exec(`
		UPDATE item SET note = ?, subtitle = ?, meta_updated_at = ?, field_updated_at = ?, updated_at = ?
		WHERE uuid = ? AND category = 'note' AND deleted = 0`,
		text, subtitle, now, now, now, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("expected to update 1 note, updated %d", n)
	}
	return nil
}
