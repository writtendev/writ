#!/usr/bin/env bash
# Checks that every commit given on the command line carries a well-formed
# Signed-off-by trailer. Shared by the dco and dco-landed jobs in
# dco.yml so the two gates check the same rule instead of carrying their
# own copies that can drift apart.
set -euo pipefail

missing=0
for sha in "$@"; do
  if ! git log -1 --format='%B' "$sha" | grep -E -qi '^Signed-off-by: .+ <.+>$'; then
    echo "::error::commit $sha has no Signed-off-by trailer (see CONTRIBUTING.md)"
    missing=1
  fi
done
exit "$missing"
