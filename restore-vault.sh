#!/bin/zsh
# Restores the local Enpass vault from a backup (enpass-env push keeps them in
# ~/.local/share/enpass-env/backups) and keeps a copy
# of the current file first.
#   restore-vault.sh ~/.local/share/enpass-env/backups/vault-YYYYMMDD-HHMMSS.enpassdb
set -euo pipefail

V="${ENPASS_VAULT:-$HOME/Library/Containers/in.sinew.Enpass-Desktop/Data/Documents/Vaults/primary}"
B="$HOME/.local/share/enpass-env/backups"
mkdir -p "$B"

if [[ $# -ne 1 ]]; then
  echo "usage: $0 BACKUP_FILE   (available backups:)" >&2
  ls -1t "$B"/vault-2*.enpassdb >&2
  exit 2
fi
GOOD="$1"
[[ -f "$GOOD" ]] || { echo "no such backup: $GOOD" >&2; exit 1; }

if pgrep -x Enpass >/dev/null; then
  echo "Enpass is running; quit it first." >&2
  exit 1
fi

cp -p "$V/vault.enpassdb" "$B/vault-broken-$(date +%Y%m%d-%H%M%S).enpassdb"
cp "$GOOD" "$V/vault.enpassdb"
chmod 644 "$V/vault.enpassdb"
cmp "$V/vault.enpassdb" "$GOOD"
echo "restored $GOOD -> $V/vault.enpassdb"
