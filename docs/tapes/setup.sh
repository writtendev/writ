#!/usr/bin/env bash
set -e

export WRIT_DEMO_ROOT=$(mktemp -d)
export WRIT_DEMO_DIR="$WRIT_DEMO_ROOT/demo"
export WRIT_BARE_DIR="$WRIT_DEMO_ROOT/remote.git"
export WRIT_COLLAB_DIR="$WRIT_DEMO_ROOT/collab"

# SSH signing key
ssh-keygen -t ed25519 -N "" -f "$WRIT_DEMO_ROOT/id_ed25519" >/dev/null 2>&1

# Bare remote
git init --bare "$WRIT_BARE_DIR" >/dev/null 2>&1

# Primary repo
mkdir -p "$WRIT_DEMO_DIR"
cd "$WRIT_DEMO_DIR"
git init -b main >/dev/null 2>&1
git config user.name "Alice"
git config user.email "alice@example.com"
git config gpg.format ssh
git config user.signingKey "$WRIT_DEMO_ROOT/id_ed25519.pub"
echo "# My Project" > README.md
git add README.md
git commit -m "Initial commit" >/dev/null 2>&1
git remote add origin "$WRIT_BARE_DIR"

# Pre-write writ.schema declaring the ticket type the tape uses: `writ init`
# never overwrites an existing one, so writing it here means the visible
# `writ schema apply` step in the tape has something real to apply, without
# making the file's own editing (not a writ command) part of the recording.
cat > writ.schema <<'SCHEMA'
namespace demo

type ticket {
  op create 1, update 1 {
    title  string  lww
  }
}
SCHEMA

# Collab repo pre-setup
git clone "$WRIT_BARE_DIR" "$WRIT_COLLAB_DIR" >/dev/null 2>&1
(
  cd "$WRIT_COLLAB_DIR"
  git config user.name "Bob"
  git config user.email "bob@example.com"
  git config gpg.format ssh
  git config user.signingKey "$WRIT_DEMO_ROOT/id_ed25519.pub"
  writ init >/dev/null 2>&1
)

# Fixed placeholder id the tape "types" literally, so the recording reads
# the same every run despite object ids being freshly minted (crypto/rand)
# each time. The wrapper below resolves a placeholder to the real id via
# `object list --json`, runs the real command against it, then masks the
# real id back out of the output. The schema object id needs no such
# masking: it is derived from the namespace (`schema:demo`, since this
# script's writ.schema declares `namespace demo`), so it already reads the
# same every run.
TICKET_PLACEHOLDER=0192a1b2c3d4e5f60718293a4b5c6d7e

# Wrapper to format object/schema ids cleanly
_real_writ=$(which writ)
writ() {
  if [ "$1" = "object" ] && [ "$2" = "create" ]; then
    out=$("$_real_writ" "$@")
    real_id="$out"
    echo "$out" | sed "s/$real_id/$TICKET_PLACEHOLDER/g"
  elif [ "$1" = "object" ] && { [ "$2" = "apply" ] || [ "$2" = "show" ]; } && [ "${3:-}" = "$TICKET_PLACEHOLDER" -o "${3:-}" = "${TICKET_PLACEHOLDER:0:8}" ]; then
    real_id=$("$_real_writ" object list ticket -C "$PWD" --json 2>/dev/null | grep -o '"object_id":"[^"]*"' | head -1 | cut -d'"' -f4)
    action="$2"
    shift 3
    if [ -n "$real_id" ]; then
      "$_real_writ" object "$action" "$real_id" "$@" | sed "s/$real_id/$TICKET_PLACEHOLDER/g"
    else
      "$_real_writ" object "$action" "$TICKET_PLACEHOLDER" "$@"
    fi
  elif [ "$1" = "object" ] && [ "$2" = "list" ]; then
    real_id=$("$_real_writ" object list ticket -C "$PWD" --json 2>/dev/null | grep -o '"object_id":"[^"]*"' | head -1 | cut -d'"' -f4)
    if [ -n "$real_id" ]; then
      "$_real_writ" "$@" | sed "s/${real_id:0:8}/${TICKET_PLACEHOLDER:0:8}/g"
    else
      "$_real_writ" "$@"
    fi
  elif [ "$1" = "init" ]; then
    "$_real_writ" "$@" | sed 's|Signing key: .*/id_ed25519.pub|Signing key: ~/.ssh/id_ed25519.pub|g'
  else
    "$_real_writ" "$@"
  fi
}

PS1="$ "
clear
