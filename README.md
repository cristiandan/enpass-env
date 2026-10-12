# enpass-env

Sync your `.env` files across machines through [Enpass](https://www.enpass.io/).

Add a variable once, `envpush`, and every other machine gets it with `envpull`.
Each env file is stored as a Secure Note in your Enpass vault, and Enpass's own
sync (Dropbox, iCloud, OneDrive, …) carries it between machines. No extra
service, no extra account. Unlocking takes one Touch ID tap.

```console
$ envpush
updated "env:myapp" to v4
  + STRIPE_WEBHOOK_SECRET
  ~ DATABASE_URL
Enpass uploads it on its next sync, which this doesn't trigger:
make any edit in Enpass so your other machines get it.

# on another machine
$ envpull
pulled .env (v4, pushed 3m ago by work-laptop)
  + STRIPE_WEBHOOK_SECRET
  ~ DATABASE_URL
previous version saved to .env.bak
```

Values are never printed: diffs and status show variable names only.

> **Unofficial.** Enpass has no API for writing to a vault, so enpass-env writes
> the note directly into the vault's encrypted database, matching exactly what
> the Enpass app writes. It has been tested with Enpass 6.12.5 on macOS. It
> backs up the vault before every write, but read [Safety](#safety) before you
> use it, and after an Enpass update try it on a throwaway project first.

## Requirements

- macOS. Unlocking uses Touch ID, or your Apple Watch or login password on Macs
  without it
- Enpass 6.8 or later for Mac, with a vault synced to a cloud provider
- Go 1.26+ and the Xcode command line tools (to build; the SQLCipher driver uses cgo)

## Install

```sh
git clone https://github.com/cristiandan/enpass-env.git
cd enpass-env
./install.sh
```

`install.sh` builds the binary, installs it to `~/.local/bin` (make sure that's
on your `PATH`) and adds `envpush`, `envpull` and `envstatus` as shortcuts.
If you have an Apple Development or Developer ID certificate it signs the
binary with it; see [Keychain prompts](#keychain-prompts).

Then unlock once, in a terminal:

```sh
enpass-env unlock
```

Type your Enpass master password. The key derived from it is stored in your
macOS Keychain, readable only by enpass-env and only after Touch ID (or your
login password). You won't type the master password again on this machine.

## Usage

```sh
envpush              # store ./.env in Enpass
envpull              # write ./.env from Enpass
envstatus            # compare ./.env with Enpass
enpass-env list      # list the env files stored in Enpass
```

Options for push, pull and status:

| Option | |
|---|---|
| `-f .env.local` | sync a different file (stored as `env:<project>/.env.local`) |
| `-project NAME` | use a different note name (see below) |
| `-force` | push or pull over a conflict |

### Which note a file syncs with

Notes are titled `env:<project>`. The project name comes from, in order:

1. the `-project` flag,
2. the `project:` line in the file's state block (below),
3. the name of the git repository the file is in (or the folder name outside git).

So in a git checkout, `envpull` usually needs no arguments, even on a new
machine. If the note is named differently from the repo, pass `-project` once.
The name is then saved in the file.

### The state block

`envpush` and `envpull` keep a comment block at the top of the env file:

```sh
# >>> enpass-env (managed by envpush/envpull; edit project to sync with another note)
# project: myapp
# note: env:myapp
# version: 4
# sha256: 8540…
# synced_at: 2026-10-12T17:23:04+01:00
# <<< enpass-env
DATABASE_URL=postgres://…
```

The block records what this machine last synced. It is never stored in Enpass
and doesn't count as a change. Everything outside it, comments included, is
synced byte for byte. To point a folder at a different note, edit `project:`.

### Conflicts

Each command compares the file, the note in Enpass, and the version recorded in
the state block:

| Changed here | Changed in Enpass | `envpush` | `envpull` |
|---|---|---|---|
| no  | no  | nothing to do | nothing to do |
| yes | no  | updates Enpass | refuses (`-force` discards local changes) |
| no  | yes | refuses: pull first | updates the file |
| yes | yes | refuses: conflict | refuses: conflict |

On a conflict you see which variables differ on each side. Merge by hand, then
`envpush -force`. `envpull` always keeps the previous file as `.env.bak`. If the
state block is missing, for example because another tool regenerated the file,
both commands treat it as a first sync and refuse when the two sides differ.

You can also edit a note directly in the Enpass app. enpass-env notices and
treats it as a change made in Enpass.

### Sync timing

Enpass doesn't notice changes written by enpass-env until its next sync. In
testing it did not sync on its own within 10 minutes. **After `envpush`, make
any small edit in Enpass** (or use its sync button) so the change uploads. On
the receiving machine, Enpass has to sync down before `envpull` can see the
change. `envpull` and `envstatus` print when Enpass last synced.

## How it's stored

Each note looks like this in Enpass:

```
enpass-env · myapp · v4
format: 1
version: 4
updated_at: 2026-10-12T16:23:04Z
updated_by: work-laptop
file: .env
repo: git@github.com:me/myapp.git
sha256: 8540…
keys: DATABASE_URL, STRIPE_WEBHOOK_SECRET
--- env ---
DATABASE_URL=postgres://…
STRIPE_WEBHOOK_SECRET=whsec_…
```

The first line is what Enpass shows in its item list, so no secret value
appears there. The header holds variable names, never values.

## Safety

- **Backups.** Before every write, the vault database is copied to
  `~/.local/share/enpass-env/backups` (the 20 most recent are kept). To roll
  back, quit Enpass and run `./restore-vault.sh <backup file>`.
- **Why writes are direct.** The Enpass app expects every column of a row to be
  filled the way it fills them itself. The note rows written here match a note
  created in the app, column for column. During development, a row with a few
  columns left empty (the way enpass-cli's `create` writes them) made Enpass
  crash on every launch until the vault was restored from a backup.
- **Enpass can stay open.** Writes were tested with Enpass running. New and
  updated notes appear immediately, and the next sync uploads them. An updated
  note wins over the older copy in the cloud.
- **Read-only diagnostics.** `enpass-env probe schema|inspect TITLE|find PREFIX`
  show the database layout and row metadata without printing secrets.

## Keychain prompts

macOS grants a Keychain item to the exact program that saved it. An unsigned
build counts as a new program every time you rebuild, so the Keychain asks for
your login password again. With a signing certificate, `install.sh` gives the
binary a stable identity: choose **Always Allow** once and only Touch ID
remains. `enpass-env forget` removes the stored key.

## Options

| Flag / variable | |
|---|---|
| `-vault DIR`, `ENPASS_VAULT` | vault directory. Default: `~/Library/Containers/in.sinew.Enpass-Desktop/Data/Documents/Vaults/primary`, or `~/Documents/Enpass/Vaults/primary` if only that exists. Set it if your vault lives elsewhere or you want a secondary vault |
| `-keyfile PATH` | keyfile, if your vault uses one |
| `-debug` | verbose logging |

## Credits

Vault decryption and Touch ID unlock are adapted from
[hazcod/enpass-cli](https://github.com/hazcod/enpass-cli) (MIT).

## License

MIT
