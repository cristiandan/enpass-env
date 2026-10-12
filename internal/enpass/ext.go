package enpass

import "database/sql"

// Additions for enpass-env (not part of upstream enpass-cli).

// DB exposes the underlying database handle.
func (v *Vault) DB() *sql.DB { return v.db }

// SetDatabaseFile points the vault at a different database file in the same
// directory (e.g. a sync copy such as dropbox.enpassdb). Call before Open.
func (v *Vault) SetDatabaseFile(path string) { v.databaseFilename = path }
