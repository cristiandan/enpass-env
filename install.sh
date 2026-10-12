#!/bin/zsh
# Builds enpass-env, signs it, and installs it with envpush/envpull/envstatus
# links into ~/.local/bin.
#
# Signing with a real certificate keeps the Keychain's "Always Allow" valid
# across rebuilds. An unsigned (ad-hoc) build is a new program to the Keychain
# every time, so it asks for your login password again.
set -euo pipefail
cd "$(dirname "$0")"

BIN="$HOME/.local/bin"
mkdir -p "$BIN"
go build -o bin/enpass-env . 2> >(grep -v -e 'warning:' -e 'sqlite3.c' -e '^ *|' -e '^ *[0-9]* |' -e 'generated' >&2)

IDENTITY="$(security find-identity -v -p codesigning | awk -F'"' '/Apple Development|Developer ID Application/ {print $2; exit}')"
if [[ -n "$IDENTITY" ]]; then
  codesign --force --sign "$IDENTITY" --identifier dev.enpass-env bin/enpass-env
  echo "signed with: $IDENTITY"
else
  echo "warning: no code signing certificate found; Keychain will ask for your password after each rebuild" >&2
fi

cp bin/enpass-env "$BIN/enpass-env"
for c in push pull status; do ln -sf enpass-env "$BIN/env$c"; done
echo "installed: $BIN/enpass-env (+ envpush, envpull, envstatus)"
