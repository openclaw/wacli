#!/usr/bin/env bash
set -euo pipefail

output_file="$(mktemp)"
trap 'rm "$output_file"' EXIT

for scope in production tests; do
  args=(-tags sqlite_fts5)
  if [[ "$scope" == tests ]]; then
    args=(-test "${args[@]}")
  fi
  if ! CGO_ENABLED=1 go run golang.org/x/tools/cmd/deadcode@v0.50.0 "${args[@]}" ./... > "$output_file"; then
    cat "$output_file"
    exit 1
  fi
  if [[ -s "$output_file" ]]; then
    printf 'Unreachable code in %s scope:\n' "$scope" >&2
    cat "$output_file"
    exit 1
  fi
done
