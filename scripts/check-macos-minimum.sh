#!/usr/bin/env bash
set -euo pipefail

binary=${1:?usage: check-macos-minimum.sh BINARY}
architectures=$(lipo -archs "$binary")
[[ -n "$architectures" ]] || { echo "No Mach-O architectures: $binary" >&2; exit 1; }
for arch in $architectures; do
  load_commands=$(otool -arch "$arch" -l "$binary")
  printf '%s\n' "$load_commands" | grep -A3 'cmd LC_BUILD_VERSION'
  minimum=$(printf '%s\n' "$load_commands" | awk '
    $1 == "cmd" { build_version = ($2 == "LC_BUILD_VERSION") }
    build_version && $1 == "minos" { print $2 }
  ')
  [[ "$minimum" == 15.0 ]] || {
    echo "Expected macOS 15.0 minimum for $binary ($arch), found: ${minimum:-missing}" >&2
    exit 1
  }
done
