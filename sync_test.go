package main

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "github.com/mutecomm/go-sqlcipher"
)

// itemSchema is the item table as created by Enpass 6.12.
const itemSchema = `CREATE TABLE item(ID INTEGER PRIMARY KEY AUTOINCREMENT,uuid TEXT UNIQUE NOT NULL,created_at INTEGER,meta_updated_at INTEGER,field_updated_at INTEGER,title TEXT,subtitle TEXT,note TEXT,icon TEXT,favorite INTEGER DEFAULT 0,trashed INTEGER DEFAULT 0,archived INTEGER DEFAULT 0,deleted INTEGER DEFAULT 0,auto_submit INTEGER DEFAULT 1,form_data TEXT DEFAULT '',category TEXT,template TEXT,wearable INTEGER DEFAULT 0,usage_count INTEGER DEFAULT 0,last_used INTEGER,key BLOB,extra TEXT DEFAULT '',updated_at INTEGER DEFAULT 0)`

const sampleEnv = "# comment\nFOO=1\nexport BAR=\"two words # not a comment\"\n\nBAZ=3\n"

func TestNoteRoundTrip(t *testing.T) {
	n := &envNote{
		Project: "proj", File: ".env", Version: 7,
		UpdatedAt: time.Date(2026, 10, 12, 15, 50, 0, 0, time.UTC),
		UpdatedBy: "host", SHA256: sha(sampleEnv), Body: sampleEnv,
	}
	text := n.render()
	if first, _, _ := strings.Cut(text, "\n"); first != "enpass-env · proj · v7" {
		t.Fatalf("first line = %q", first)
	}
	if strings.Contains(strings.Split(text, bodyMarker)[0], "two words") {
		t.Fatal("header leaks a value")
	}
	got, err := parseNote(text)
	if err != nil {
		t.Fatal(err)
	}
	if got.Body != sampleEnv || got.Version != 7 || got.UpdatedBy != "host" ||
		!got.UpdatedAt.Equal(n.UpdatedAt) || got.EditedInApp() {
		t.Fatalf("round trip mismatch: %+v", got)
	}

	edited, _ := parseNote(strings.Replace(text, "BAZ=3", "BAZ=4", 1))
	if !edited.EditedInApp() {
		t.Fatal("edit in app not detected")
	}

	empty := &envNote{Project: "p", File: ".env", Version: 1, SHA256: sha("")}
	if got, err := parseNote(strings.TrimSuffix(empty.render(), "\n")); err != nil || got.Body != "" {
		t.Fatalf("empty body: %v %+v", err, got)
	}
	if _, err := parseNote("just a note"); err == nil {
		t.Fatal("expected error for foreign note")
	}
}

func TestDiffEnv(t *testing.T) {
	d := diffEnv(sampleEnv, "FOO=1\nBAR=changed\nNEW=x\n")
	want := keyDiff{Added: []string{"NEW"}, Removed: []string{"BAZ"}, Changed: []string{"BAR"}}
	if !reflect.DeepEqual(d, want) {
		t.Fatalf("got %+v", d)
	}
	if dups := duplicateKeys("A=1\nB=2\nA=3\n"); !reflect.DeepEqual(dups, []string{"A"}) {
		t.Fatalf("dups = %v", dups)
	}
}

func TestStatus(t *testing.T) {
	base, local, remote := "A=1\n", "A=2\n", "A=3\n"
	note := func(body string) *envNote { return &envNote{Body: body} }
	state := &syncState{SHA256: sha(base)}
	cases := []struct {
		name string
		c    syncCtx
		want syncStatus
	}{
		{"no remote", syncCtx{hasLocal: true, local: local}, noRemote},
		{"no local", syncCtx{note: note(remote)}, noLocal},
		{"same", syncCtx{hasLocal: true, local: remote, note: note(remote), state: state}, upToDate},
		{"local ahead", syncCtx{hasLocal: true, local: local, note: note(base), state: state}, localAhead},
		{"remote ahead", syncCtx{hasLocal: true, local: base, note: note(remote), state: state}, remoteAhead},
		{"both", syncCtx{hasLocal: true, local: local, note: note(remote), state: state}, conflict},
		{"no history", syncCtx{hasLocal: true, local: local, note: note(remote)}, conflict},
	}
	for _, tc := range cases {
		if got := tc.c.status(); got != tc.want {
			t.Errorf("%s: got %d want %d", tc.name, got, tc.want)
		}
	}
}

func TestVaultWrites(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "v.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(itemSchema); err != nil {
		t.Fatal(err)
	}

	title := noteTitle("proj", ".env")
	if n, err := findNote(db, title); err != nil || n != nil {
		t.Fatalf("expected none: %v %v", n, err)
	}

	v1 := &envNote{Project: "proj", File: ".env", Version: 1, SHA256: sha(sampleEnv), Body: sampleEnv}
	if err := insertNote(db, title, v1.render()); err != nil {
		t.Fatal(err)
	}

	// Every column must be non-NULL and match what the app writes for a note.
	var nulls int
	var category, template, icon, subtitle, keyType string
	var keyLen, autoSubmit, lastUsed int
	var created, metaUpd, fieldUpd, upd int64
	err = db.QueryRow(`SELECT
		(uuid IS NULL)+(created_at IS NULL)+(meta_updated_at IS NULL)+(field_updated_at IS NULL)+
		(title IS NULL)+(subtitle IS NULL)+(note IS NULL)+(icon IS NULL)+(favorite IS NULL)+
		(trashed IS NULL)+(archived IS NULL)+(deleted IS NULL)+(auto_submit IS NULL)+(form_data IS NULL)+
		(category IS NULL)+(template IS NULL)+(wearable IS NULL)+(usage_count IS NULL)+(last_used IS NULL)+
		(key IS NULL)+(extra IS NULL)+(updated_at IS NULL),
		category, template, icon, subtitle, typeof(key), length(key), auto_submit, last_used,
		created_at, meta_updated_at, field_updated_at, updated_at
		FROM item WHERE title = ?`, title).Scan(&nulls, &category, &template, &icon, &subtitle,
		&keyType, &keyLen, &autoSubmit, &lastUsed, &created, &metaUpd, &fieldUpd, &upd)
	if err != nil {
		t.Fatal(err)
	}
	if nulls != 0 || category != "note" || template != "note.default" || keyType != "blob" || keyLen != 44 ||
		autoSubmit != 1 || lastUsed != 0 || subtitle != "enpass-env · proj · v1" ||
		icon != `{"fav":"","image":{"file":"misc/secure_note"},"type":1,"uuid":""}` ||
		created == 0 || metaUpd != created || fieldUpd != created || upd != created {
		t.Fatalf("row not app-shaped: nulls=%d %s %s %s %q key=%s/%d", nulls, category, template, icon, subtitle, keyType, keyLen)
	}

	got, err := findNote(db, title)
	if err != nil || got == nil || got.Body != sampleEnv || got.Version != 1 {
		t.Fatalf("find after insert: %+v %v", got, err)
	}

	v2 := *v1
	v2.Version, v2.Body = 2, sampleEnv+"NEW=1\n"
	v2.SHA256 = sha(v2.Body)
	if err := updateNote(db, got.UUID, v2.render()); err != nil {
		t.Fatal(err)
	}
	got, _ = findNote(db, title)
	if got.Version != 2 || got.Body != v2.Body {
		t.Fatalf("after update: %+v", got)
	}

	// Trashed notes are ignored; duplicates are an error.
	if _, err := db.Exec(`UPDATE item SET trashed = 1`); err != nil {
		t.Fatal(err)
	}
	if n, _ := findNote(db, title); n != nil {
		t.Fatal("trashed note returned")
	}
	_ = insertNote(db, title, v1.render())
	_ = insertNote(db, title, v1.render())
	if _, err := findNote(db, title); err == nil {
		t.Fatal("expected duplicate error")
	}
	if l, _ := listNotes(db); len(l) != 2 {
		t.Fatalf("list = %d", len(l))
	}
}

func TestStateBlock(t *testing.T) {
	st := &syncState{
		Project: "proj", Title: "env:proj", Version: 3, SHA256: sha(sampleEnv),
		SyncedAt: time.Date(2026, 10, 12, 16, 30, 0, 0, time.UTC),
	}
	file := renderBlock(st) + sampleEnv
	got, rest := splitBlock(file)
	if rest != sampleEnv || !got.SyncedAt.Equal(st.SyncedAt) {
		t.Fatalf("round trip: %+v %q", got, rest)
	}
	got.SyncedAt = st.SyncedAt
	if !reflect.DeepEqual(got, st) {
		t.Fatalf("round trip: %+v %q", got, rest)
	}
	if _, rest := splitBlock(sampleEnv); rest != sampleEnv {
		t.Fatal("content without a block changed")
	}

	// A block moved below other lines, with the project edited by hand.
	moved := "FOO=1\n" + strings.Replace(renderBlock(st), "project: proj", "project:  other ", 1) + "BAR=2\n"
	got, rest = splitBlock(moved)
	if rest != "FOO=1\nBAR=2\n" || got.Project != "other" {
		t.Fatalf("moved block: %+v %q", got, rest)
	}

	// An unterminated block is left as content.
	broken := blockBegin + "\n# project: x\nFOO=1\n"
	if got, rest := splitBlock(broken); got != nil || rest != broken {
		t.Fatal("unterminated block parsed")
	}

	if p, rest := stripLegacyProject("# enpass-env: project=old\n" + sampleEnv); p != "old" || rest != sampleEnv {
		t.Fatalf("legacy: %q %q", p, rest)
	}
	if p, _ := stripLegacyProject("FOO=1 # enpass-env: project=x\n"); p != "" {
		t.Fatal("matched a non-comment line")
	}
}

func TestCheckWritable(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "v.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(itemSchema); err != nil {
		t.Fatal(err)
	}
	if err := checkWritable(db, 6); err != nil {
		t.Fatalf("tested layout rejected: %v", err)
	}
	if err := checkWritable(db, 7); err == nil || !strings.Contains(err.Error(), "version is 7") {
		t.Fatalf("vault version not checked: %v", err)
	}
	if _, err := db.Exec(`ALTER TABLE item ADD COLUMN pinned INTEGER`); err != nil {
		t.Fatal(err)
	}
	if err := checkWritable(db, 6); err == nil || !strings.Contains(err.Error(), "pinned INTEGER") {
		t.Fatalf("new column not detected: %v", err)
	}
}
