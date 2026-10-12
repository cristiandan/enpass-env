`enpass/` and `unlock/` are copied from https://github.com/hazcod/enpass-cli
(MIT, see LICENSE-enpass-cli) at commit 76beaa9 (v1.14.0).

Local changes:
- `enpass/ext.go` added (DB and vault info accessors, database file override).
- `enpass/vault.go`: waits up to 10s when the Enpass app holds a lock (`_busy_timeout`).
- `enpass/write.go` dropped: its inserts leave columns NULL that Enpass 6.12 crashes on.
- `unlock/biometric_store_darwin.go`: keychain service renamed to `enpass-env`.
- `unlock/biometric_auth_darwin.m`: accepts Apple Watch or the login password when Touch ID is unavailable.
