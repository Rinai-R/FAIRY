#!/bin/bash

# Run every Go test introduced by the integration build tag in its own test
# process. The embedded SeekDB engine intentionally lives until its host exits,
# so multiple fresh DataDir scenarios cannot share one package test process.

set -euo pipefail

usage() {
  cat <<'EOF'
Usage: test-integration-all.sh

FAIRY_SEEKDB_LIBRARY must name the real in-process SeekDB shared library.
Each integration-tagged top-level test runs in a fresh Go test process.
EOF
}

die() {
  echo "FAIRY SeekDB integration suite: $*" >&2
  exit 1
}

if (($# > 0)); then
  case "$1" in
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "unknown argument: $1"
      ;;
  esac
fi
(($# == 0)) || die "unexpected arguments"

library="${FAIRY_SEEKDB_LIBRARY:-}"
[[ "$library" == /* && -f "$library" && ! -L "$library" ]] || die "FAIRY_SEEKDB_LIBRARY must name an absolute regular non-symlink library"

for tool in awk comm go grep mkdir mktemp rm sort; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool is required"
done

module_root="$(cd "$(dirname "$0")/../../.." && pwd -P)"
cd "$module_root"

scratch_base="${TMPDIR:-/tmp}"
scratch_base="${scratch_base%/}"
scratch="$(mktemp -d "$scratch_base/fairy-seekdb-integration.XXXXXX")"
scratch="$(cd "$scratch" && pwd -P)"
trap 'rm -rf "$scratch"' EXIT INT TERM

ordinary="$scratch/ordinary"
integrated="$scratch/integrated"
selected="$scratch/selected"
packages="$scratch/packages"
ordinary_packages="$scratch/ordinary-packages"
total=0

go list ./... | sort -u >"$ordinary_packages"
go list -tags=integration ./... | sort -u >"$packages"
while IFS= read -r package; do
  [[ -n "$package" ]] || continue
  if grep -Fxq "$package" "$ordinary_packages"; then
    go test -list '^Test' "$package" | awk '/^Test[[:alnum:]_]+$/ {print}' | sort -u >"$ordinary"
  else
    : >"$ordinary"
  fi
  go test -tags=integration -list '^Test' "$package" | awk '/^Test[[:alnum:]_]+$/ {print}' | sort -u >"$integrated"
  comm -13 "$ordinary" "$integrated" >"$selected"

  while IFS= read -r test_name; do
    [[ -n "$test_name" ]] || continue
    total=$((total + 1))
    test_root="$scratch/test-$total"
    mkdir -m 700 "$test_root"
    printf '=== SEEKDB INTEGRATION %s %s\n' "$package" "$test_name"
    FAIRY_SEEKDB_INTEGRATION_DATA_ROOT="$test_root" \
      go test -tags=integration -count=1 -run "^${test_name}$" "$package" || die "$package $test_name failed"
    [[ "$test_root" == "$scratch"/test-* ]] || die "refusing unexpected cleanup target: $test_root"
    rm -rf -- "$test_root"
  done <"$selected"
done <"$packages"

((total > 0)) || die "no integration-tagged tests were discovered"
printf 'FAIRY SeekDB isolated integration suite: PASS (%s tests)\n' "$total"
