package main

// Read-only diagnostics of the vault database. Never prints secret values.

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/cristiandan/enpass-env/internal/enpass"
)

// Columns whose contents are secret or bulky: printed as a length only.
var redacted = map[string]bool{
	"key": true, "value": true, "note": true, "form_data": true,
	"history": true, "initial": true, "hash": true, "extra": true,
	"private_key": true, "password": true, "data": true, "user_handle": true, "credential_id": true,
}

func probe(o opts, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("probe needs a subcommand")
	}
	switch args[0] {
	case "schema":
		return withVault(o, func(v *enpass.Vault) error { return probeSchema(v.DB()) })
	case "recent":
		return withVault(o, func(v *enpass.Vault) error {
			rows, err := v.DB().Query(`SELECT uuid, title, category, template, created_at, updated_at, trashed, deleted FROM item WHERE created_at > strftime('%s','now') - 1800 OR updated_at > strftime('%s','now') - 1800`)
			if err != nil {
				return err
			}
			_, err = dumpRows(rows, "item", nil)
			return err
		})
	case "meta":
		return withVault(o, func(v *enpass.Vault) error {
			db := v.DB()
			for _, q := range []string{
				`SELECT ID, Version, Sync_UUID, length(Signature) AS signature_len, length(Hash) AS hash_len, length(Info) AS info_len FROM Identity`,
				`SELECT ID, vault_uuid, deleted, updated_at FROM vault_info`,
				`SELECT verison_key, verison_value FROM versions`,
				`SELECT vault, key, length(value) AS value_len FROM preferences`,
				`SELECT name, seq FROM sqlite_sequence`,
				`SELECT count(*) AS items, sum(trashed) AS trashed, sum(deleted) AS deleted FROM item`,
			} {
				rows, err := db.Query(q)
				if err != nil {
					return err
				}
				if _, err := dumpRows(rows, strings.Fields(q)[len(strings.Fields(q))-1], nil); err != nil {
					return err
				}
			}
			var same bool
			if err := db.QueryRow(`SELECT note = subtitle FROM item WHERE title = 'env-sync-ref'`).Scan(&same); err == nil {
				fmt.Println("env-sync-ref: note == subtitle:", same)
			}
			return nil
		})
	case "types":
		return withVault(o, func(v *enpass.Vault) error {
			cols := []string{"uuid", "created_at", "meta_updated_at", "field_updated_at", "title", "subtitle", "note", "icon", "favorite", "trashed", "archived", "deleted", "auto_submit", "form_data", "category", "template", "wearable", "usage_count", "last_used", "key", "extra", "updated_at"}
			sel := make([]string, len(cols))
			for i, c := range cols {
				sel[i] = "typeof(" + c + ") AS t_" + c
			}
			rows, err := v.DB().Query(`SELECT title, `+strings.Join(sel, ", ")+` FROM item WHERE title IN (`+strings.TrimSuffix(strings.Repeat("?,", len(args)-1), ",")+`)`, toAny(args[1:])...)
			if err != nil {
				return err
			}
			_, err = dumpRows(rows, "types", nil)
			return err
		})
	case "find":
		if len(args) != 2 {
			return fmt.Errorf("usage: probe find TITLE-PREFIX")
		}
		return withVault(o, func(v *enpass.Vault) error {
			rows, err := v.DB().Query(`SELECT uuid, title, category, template, trashed, deleted, updated_at FROM item WHERE title LIKE ? ESCAPE '\'`, strings.NewReplacer("%", `\%`, "_", `\_`).Replace(args[1])+"%")
			if err != nil {
				return err
			}
			_, err = dumpRows(rows, "item", nil)
			return err
		})
	case "inspect":
		if len(args) != 2 {
			return fmt.Errorf("usage: probe inspect TITLE")
		}
		return withVault(o, func(v *enpass.Vault) error { return probeInspect(v.DB(), args[1]) })
	default:
		return fmt.Errorf("unknown probe subcommand %q", args[0])
	}
}

func probeSchema(db *sql.DB) error {
	rows, err := db.Query(`SELECT name, sql FROM sqlite_master WHERE type IN ('table','trigger','index') ORDER BY type, name`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var ddl sql.NullString
		if err := rows.Scan(&name, &ddl); err != nil {
			return err
		}
		fmt.Printf("-- %s\n%s\n\n", name, ddl.String)
	}
	return rows.Err()
}

func probeInspect(db *sql.DB, title string) error {
	items, err := db.Query(`SELECT * FROM item WHERE title = ?`, title)
	if err != nil {
		return err
	}
	var uuids []string
	n, err := dumpRows(items, "item", func(row map[string]any) {
		uuids = append(uuids, asString(row["uuid"]))
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("no item titled %q", title)
	}
	for _, u := range uuids {
		for _, t := range itemTables {
			rows, err := db.Query(`SELECT * FROM `+t+` WHERE item_uuid = ?`, u)
			if err != nil {
				return err
			}
			if _, err := dumpRows(rows, t+" "+u, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// Tables with rows keyed by item_uuid.
var itemTables = []string{"itemfield", "identical_passwords", "folder_items", "attachment", "passkeys"}

func dumpRows(rows *sql.Rows, label string, each func(map[string]any)) (int, error) {
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return 0, err
	}
	n := 0
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return n, err
		}
		n++
		row := map[string]any{}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintf(tw, "== %s #%d\n", label, n)
		for i, c := range cols {
			row[c] = vals[i]
			fmt.Fprintf(tw, "  %s\t%s\n", c, show(c, vals[i]))
		}
		tw.Flush()
		if each != nil {
			each(row)
		}
	}
	return n, rows.Err()
}

func show(col string, v any) string {
	if v == nil {
		return "NULL"
	}
	if b, ok := v.([]byte); ok {
		if redacted[col] {
			return fmt.Sprintf("<%d bytes>", len(b))
		}
		return strings.TrimSpace(string(b))
	}
	if s, ok := v.(string); ok && redacted[col] {
		return fmt.Sprintf("<%d chars>", len(s))
	}
	return fmt.Sprint(v)
}

func asString(v any) string {
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return fmt.Sprint(v)
}

func toAny(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}
