#!/usr/bin/env bash
set -euo pipefail

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/wacli-lock-windows.XXXXXX")"
trap 'rm -r "$tmp_dir"' EXIT

GOOS=windows GOARCH=amd64 go test -c ./internal/lock -o "$tmp_dir/lock.test.exe"

# Compile the store tests without a Windows C compiler; sqlite uses its stub.
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go test -c ./internal/store -o "$tmp_dir/store.test.exe"
