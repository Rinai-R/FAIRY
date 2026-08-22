#!/bin/bash

# Dynamically verify the bundled in-process SeekDB runtime from an assembled
# FAIRY.app. The executable writes a durable marker only after Open, every
# migration, CheckSchema, and Close return to the Go host. This catches the
# upstream embedded-engine failure mode that can terminate a helper with
# _Exit(0) before Go assertions run.

set -euo pipefail

die() {
  echo "FAIRY SeekDB runtime verification: $*" >&2
  exit 1
}

[[ $# -eq 2 && "$1" == "--app" ]] || die "usage: verify-seekdb-runtime-darwin.sh --app PATH"
app="$2"
[[ "$(uname -s)" == "Darwin" && "$(uname -m)" == "arm64" ]] || die "verification requires darwin/arm64"
[[ -d "$app/Contents" && "${app##*.}" == "app" ]] || die "--app must name an assembled .app bundle"
executable="$app/Contents/MacOS/FAIRY"
[[ -x "$executable" && ! -L "$executable" ]] || die "packaged FAIRY executable is missing, non-executable, or symlinked"

tmp_base="${TMPDIR:-/tmp}"
tmp_base="${tmp_base%/}"
[[ -n "$tmp_base" && "$tmp_base" == /* ]] || die "TMPDIR must name an absolute directory"
root="$(mktemp -d "$tmp_base/fairy-seekdb-runtime.XXXXXX")"
chmod 700 "$root"
case "$root" in
  "$tmp_base"/fairy-seekdb-runtime.*) ;;
  *) die "mktemp returned an unexpected verification path" ;;
esac
cleanup() {
  if [[ -n "${helper_pid:-}" ]] && kill -0 "$helper_pid" 2>/dev/null; then
    kill -KILL "$helper_pid" 2>/dev/null || true
    wait "$helper_pid" 2>/dev/null || true
  fi
  rm -rf -- "$root"
}
trap cleanup EXIT INT TERM

marker="$root/host-completed"
env -i \
  HOME="$root" \
  TMPDIR="$tmp_base" \
  PATH="/usr/bin:/bin:/usr/sbin:/sbin" \
  "$executable" --verify-seekdb-runtime "$root" &
helper_pid=$!

# The Go host writes the marker only after the full endpoint runtime has
# opened, verified its local state, and returned from Close. SeekDB v1.3 can
# then hang in C++ static destruction while joining already-closed background
# threads, so waiting only for process exit would make the release gate
# unbounded. Observe both outcomes: an early exit without the marker is a
# failure, while a durable valid marker authorizes bounded process reaping.
verification_deadline=$((SECONDS + 190))
while kill -0 "$helper_pid" 2>/dev/null && [[ ! -f "$marker" ]]; do
  if (( SECONDS >= verification_deadline )); then
    die "helper timed out before writing the host-completion marker"
  fi
  sleep 0.1
done

[[ -f "$marker" && ! -L "$marker" ]] || die "helper exited before writing the host-completion marker"
[[ "$(cat "$marker")" == "completed" ]] || die "host-completion marker is invalid"

forced_reap=0
if kill -0 "$helper_pid" 2>/dev/null; then
  shutdown_deadline=$((SECONDS + 2))
  while kill -0 "$helper_pid" 2>/dev/null && (( SECONDS < shutdown_deadline )); do
    sleep 0.1
  done
fi
if kill -0 "$helper_pid" 2>/dev/null; then
  forced_reap=1
  kill -TERM "$helper_pid" 2>/dev/null || true
  term_deadline=$((SECONDS + 2))
  while kill -0 "$helper_pid" 2>/dev/null && (( SECONDS < term_deadline )); do
    sleep 0.1
  done
fi
if kill -0 "$helper_pid" 2>/dev/null; then
  kill -KILL "$helper_pid" 2>/dev/null || true
fi
if wait "$helper_pid" 2>/dev/null; then
  helper_status=0
else
  helper_status=$?
fi
helper_pid=""
if (( forced_reap == 0 && helper_status != 0 )); then
  die "helper failed after writing the host-completion marker"
fi
echo "FAIRY SeekDB runtime verification: PASS"
