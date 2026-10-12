package main

import (
	"database/sql"
	"fmt"
	"strings"
)

// testedItemColumns is the item table layout (name and declared type) of the
// Enpass versions enpass-env's writes were tested against. Rows Enpass doesn't
// expect can crash it, so writes are refused when the layout differs.
var testedItemColumns = []string{
	"ID INTEGER", "uuid TEXT", "created_at INTEGER", "meta_updated_at INTEGER",
	"field_updated_at INTEGER", "title TEXT", "subtitle TEXT", "note TEXT",
	"icon TEXT", "favorite INTEGER", "trashed INTEGER", "archived INTEGER",
	"deleted INTEGER", "auto_submit INTEGER", "form_data TEXT", "category TEXT",
	"template TEXT", "wearable INTEGER", "usage_count INTEGER", "last_used INTEGER",
	"key BLOB", "extra TEXT", "updated_at INTEGER",
}

const (
	testedVaultVersion = 6
	testedEnpass       = "Enpass 6.12"
	issuesURL          = "https://github.com/cristiandan/enpass-env/issues"
)

// checkWritable returns an error unless the vault looks exactly like the ones
// writes were tested on. Reading is unaffected.
func checkWritable(db *sql.DB, vaultVersion int) error {
	var problems []string
	if vaultVersion != testedVaultVersion {
		problems = append(problems, fmt.Sprintf("vault.json version is %d, expected %d", vaultVersion, testedVaultVersion))
	}

	rows, err := db.Query(`SELECT name, type FROM pragma_table_info('item') ORDER BY cid`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			return err
		}
		got = append(got, name+" "+strings.ToUpper(typ))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if added, removed := setDiff(got, testedItemColumns), setDiff(testedItemColumns, got); len(added)+len(removed) > 0 {
		if len(added) > 0 {
			problems = append(problems, "item table has new columns: "+strings.Join(added, ", "))
		}
		if len(removed) > 0 {
			problems = append(problems, "item table lacks columns: "+strings.Join(removed, ", "))
		}
	} else if strings.Join(got, ",") != strings.Join(testedItemColumns, ",") {
		problems = append(problems, "item table columns are in a different order")
	}

	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("refusing to write: this vault differs from the %s layout enpass-env was tested with,\n"+
		"and Enpass can crash on rows it doesn't expect:\n  - %s\n"+
		"pull, status and list still work. Please report this at %s",
		testedEnpass, strings.Join(problems, "\n  - "), issuesURL)
}

// setDiff returns the elements of a not in b.
func setDiff(a, b []string) []string {
	in := map[string]bool{}
	for _, s := range b {
		in[s] = true
	}
	var out []string
	for _, s := range a {
		if !in[s] {
			out = append(out, s)
		}
	}
	return out
}
