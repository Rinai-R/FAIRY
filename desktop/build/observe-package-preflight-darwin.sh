#!/bin/bash

# Observe an assembled FAIRY.app before Developer ID signing/notarization.
#
# This is deliberately weaker than observe-release-darwin.sh. It never accepts
# provider origins or credentials, always uses a private empty HOME, and can
# only emit result=preflight_pass with release_eligible=false. Its purpose is
# to catch package/runtime boundary regressions on a developer machine without
# allowing unsigned evidence to be mistaken for release evidence.

set -euo pipefail

usage() {
  cat <<'EOF'
Usage: observe-package-preflight-darwin.sh \
  --app PATH \
  --output DIRECTORY \
  [--duration SECONDS] \
  [--runs COUNT]

The App is launched with a private empty HOME, a system-only PATH, and fake
hostile provider/profile/proxy environment overrides. The preflight verifies
the assembled package and bundled SeekDB, then records child processes,
listeners, network sockets, and dynamic libraries across at least two launches.
It does not verify Developer ID signing, notarization, Gatekeeper, or live
third-party providers and therefore cannot produce release PASS evidence.
EOF
}

die() {
  echo "FAIRY package preflight: $*" >&2
  exit 1
}

app=""
output=""
duration=8
runs=2

while (($# > 0)); do
  case "$1" in
    --app)
      (($# >= 2)) || die "--app requires a path"
      app="$2"
      shift 2
      ;;
    --output)
      (($# >= 2)) || die "--output requires a directory"
      output="$2"
      shift 2
      ;;
    --duration)
      (($# >= 2)) || die "--duration requires seconds"
      duration="$2"
      shift 2
      ;;
    --runs)
      (($# >= 2)) || die "--runs requires a count"
      runs="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "unknown argument: $1"
      ;;
  esac
done

[[ "$(uname -s)" == "Darwin" ]] || die "this preflight requires macOS"
[[ -n "$app" && "$app" != *$'\t'* && "$app" != *$'\n'* && -d "$app/Contents" && "${app##*.}" == "app" ]] || die "--app must name an assembled .app bundle"
[[ -n "$output" && "$output" != *$'\t'* && "$output" != *$'\n'* && ! -e "$output" ]] || die "--output must name a new directory"
[[ "$duration" =~ ^[0-9]+$ && "$duration" -ge 5 && "$duration" -le 300 ]] || die "--duration must be 5..300 seconds"
[[ "$runs" =~ ^[0-9]+$ && "$runs" -ge 2 && "$runs" -le 10 ]] || die "--runs must be 2..10"

# lsof reports absolute paths. Normalize the caller-supplied App path before
# comparing observed files with the packaged SeekDB library so relative paths
# cannot turn a declared in-bundle dependency into a false violation.
app_parent="$(cd "$(dirname "$app")" && pwd -P)"
app="$app_parent/${app##*/}"

for tool in awk cat codesign env kill lsof mkdir mktemp osascript plutil ps rm rmdir sed shasum sleep sort sw_vers uname; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool is required"
done

executable="$app/Contents/MacOS/FAIRY"
library="$app/Contents/Frameworks/libseekdb.dylib"
info_plist="$app/Contents/Info.plist"
runtime_verifier="$(dirname "$0")/verify-seekdb-runtime-darwin.sh"
[[ -x "$executable" && ! -L "$executable" ]] || die "packaged FAIRY executable is missing, non-executable, or symlinked"
[[ -f "$library" && ! -L "$library" ]] || die "packaged libseekdb.dylib is missing or symlinked"
[[ -f "$info_plist" && ! -L "$info_plist" ]] || die "packaged Info.plist is missing or symlinked"
[[ -x "$runtime_verifier" && ! -L "$runtime_verifier" ]] || die "packaged SeekDB runtime verifier is missing"

bundle_id="$(plutil -extract CFBundleIdentifier raw -o - "$info_plist")"
[[ -n "$bundle_id" && "$bundle_id" != *$'\t'* && "$bundle_id" != *$'\n'* ]] || die "packaged bundle identifier is invalid"
host_os_version="$(sw_vers -productVersion)"
host_arch="$(uname -m)"
app_minimum_system_version="$(plutil -extract LSMinimumSystemVersion raw -o - "$info_plist")"
for value in "$host_os_version" "$host_arch" "$app_minimum_system_version"; do
  [[ -n "$value" && "$value" != *$'\t'* && "$value" != *$'\n'* ]] || die "host or App platform metadata is invalid"
done
[[ "$host_os_version" =~ ^[0-9]+([.][0-9]+){0,2}$ ]] || die "host macOS version is invalid"
[[ "$app_minimum_system_version" =~ ^[0-9]+([.][0-9]+){0,2}$ ]] || die "App minimum macOS version is invalid"
if [[ "$(osascript -e "application id \"$bundle_id\" is running" 2>/dev/null || true)" == "true" ]]; then
  die "an App with the packaged bundle identifier is already running"
fi

mkdir -m 700 "$output"
scratch="$(mktemp -d "${TMPDIR:-/tmp}/fairy-package-preflight.XXXXXX")"
private_home="$scratch/home"
mkdir -m 700 "$private_home"
app_pid=""
current_run=0

cleanup() {
  if [[ -n "$app_pid" ]] && kill -0 "$app_pid" 2>/dev/null; then
    osascript -e "tell application id \"$bundle_id\" to quit" >/dev/null 2>&1 || true
    kill -TERM "$app_pid" >/dev/null 2>&1 || true
  fi
  rm -rf "$scratch"
}
trap cleanup EXIT INT TERM

metadata="$output/metadata.tsv"
children="$output/child-processes.tsv"
listeners="$output/listeners.tsv"
network="$output/network-sockets.tsv"
dylibs="$output/dynamic-libraries.tsv"
violations="$output/violations.tsv"

metadata_set() {
  local key="$1"
  local value="$2"
  local next="$scratch/metadata.next"
  awk -F '\t' -v OFS='\t' -v key="$key" -v value="$value" '
    NR == 1 { print; next }
    $1 == key {
      if (!replaced) {
        print key, value
        replaced = 1
      }
      next
    }
    { print }
    END {
      if (!replaced) print key, value
    }
  ' "$metadata" >"$next"
  cat "$next" >"$metadata"
}

version_at_least() {
  awk -v actual="$1" -v minimum="$2" 'BEGIN {
    split(actual, actual_parts, ".")
    split(minimum, minimum_parts, ".")
    for (part = 1; part <= 3; part++) {
      actual_part = actual_parts[part] + 0
      minimum_part = minimum_parts[part] + 0
      if (actual_part > minimum_part) exit 0
      if (actual_part < minimum_part) exit 1
    }
    exit 0
  }'
}

printf 'key\tvalue\n' >"$metadata"
printf 'schema_version\t1\n' >>"$metadata"
printf 'verification_level\tunsigned_preflight\n' >>"$metadata"
printf 'result\tpreflight_fail\n' >>"$metadata"
printf 'release_eligible\tfalse\n' >>"$metadata"
printf 'package_verification\tfail\n' >>"$metadata"
printf 'host_platform_supported\tfalse\n' >>"$metadata"
printf 'developer_id_checked\tfalse\n' >>"$metadata"
printf 'notarization_checked\tfalse\n' >>"$metadata"
printf 'gatekeeper_checked\tfalse\n' >>"$metadata"
printf 'provider_smoke_checked\tfalse\n' >>"$metadata"
printf 'bundle_identifier\t%s\n' "$bundle_id" >>"$metadata"
printf 'host_os_version\t%s\n' "$host_os_version" >>"$metadata"
printf 'host_arch\t%s\n' "$host_arch" >>"$metadata"
printf 'app_minimum_system_version\t%s\n' "$app_minimum_system_version" >>"$metadata"
printf 'profile\tendpoint-strict\n' >>"$metadata"
printf 'duration_seconds\t%s\n' "$duration" >>"$metadata"
printf 'runs\t%s\n' "$runs" >>"$metadata"
printf 'seekdb_telemetry_override_attempt\ttrue\n' >>"$metadata"
printf 'provider_environment_override_attempt\ttrue\n' >>"$metadata"
printf 'executable_sha256\t%s\n' "$(shasum -a 256 "$executable" | awk '{print $1}')" >>"$metadata"
printf 'seekdb_sha256\t%s\n' "$(shasum -a 256 "$library" | awk '{print $1}')" >>"$metadata"
printf 'run\tpid\tppid\texecutable\n' >"$children"
printf 'run\tprotocol\tlocal_endpoint\n' >"$listeners"
printf 'run\tprotocol\tendpoint\tstate\n' >"$network"
printf 'run\tpath\n' >"$dylibs"
printf 'run\tcode\n' >"$violations"

[[ "$host_arch" == "arm64" ]] || die "host architecture $host_arch is unsupported; want arm64"
version_at_least "$host_os_version" "$app_minimum_system_version" || die "host macOS $host_os_version is older than App minimum $app_minimum_system_version"
metadata_set host_platform_supported true

# Ad-hoc signatures are acceptable for this developer preflight, but the
# assembled code graph must still be structurally valid.
codesign --verify --deep --strict --verbose=2 "$app" >/dev/null
"$executable" --verify-package-layout
"$runtime_verifier" --app "$app"
metadata_set package_verification pass

record_children() {
  local run="$1"
  local pid="$2"
  ps -axo pid=,ppid=,comm= | awk -v root="$pid" -v run="$run" '
    {
      child = $1
      parent[child] = $2
      $1 = ""
      $2 = ""
      sub(/^[[:space:]]+/, "")
      count = split($0, path, "/")
      executable[child] = path[count]
      gsub(/[^A-Za-z0-9._-]/, "?", executable[child])
    }
    END {
      for (candidate in parent) {
        current = candidate
        for (depth = 0; depth < 256; depth++) {
          if (parent[current] == root) {
            print run "\t" candidate "\t" parent[candidate] "\t" executable[candidate]
            break
          }
          if (!(current in parent) || parent[current] == current || parent[current] == 0) break
          current = parent[current]
        }
      }
    }
  ' >>"$children"
}

record_network() {
  local run="$1"
  local pid="$2"
  local protocol endpoint state
  lsof -nP -a -p "$pid" -i 2>/dev/null | awk 'NR > 1 {print $8 "\t" $9 "\t" $10}' >"$scratch/network" || true
  while IFS=$'\t' read -r protocol endpoint state; do
    [[ -n "$protocol" && -n "$endpoint" ]] || continue
    printf '%s\t%s\t%s\t%s\n' "$run" "$protocol" "$endpoint" "$state" >>"$network"
    if [[ "$state" == "(LISTEN)" ]]; then
      printf '%s\t%s\t%s\n' "$run" "$protocol" "$endpoint" >>"$listeners"
      printf '%s\tunexpected_listener\n' "$run" >>"$violations"
    else
      printf '%s\tunexpected_network_socket\n' "$run" >>"$violations"
    fi
  done <"$scratch/network"
}

record_dylibs() {
  local run="$1"
  local pid="$2"
  local name normalized
  lsof -nP -a -p "$pid" -Fn 2>/dev/null | sed -n 's/^n//p' >"$scratch/files" || true
  while IFS= read -r name; do
    case "$name" in
      *.dylib|*.framework/*) ;;
      *) continue ;;
    esac
    normalized="$name"
    case "$normalized" in
      /System/Volumes/Preboot/Cryptexes/OS/*) normalized="${normalized#/System/Volumes/Preboot/Cryptexes/OS}" ;;
    esac
    case "$normalized" in
      /System/Library/*|/usr/lib/*) ;;
      "$library") normalized='@app/Contents/Frameworks/libseekdb.dylib' ;;
      *) printf '%s\tundeclared_dynamic_library\n' "$run" >>"$violations" ;;
    esac
    printf '%s\t%s\n' "$run" "$normalized" >>"$dylibs"
  done <"$scratch/files"
}

quit_app() {
  local pid="$1"
  local deadline
  osascript -e "tell application id \"$bundle_id\" to quit" >/dev/null 2>&1 || true
  kill -TERM "$pid" >/dev/null 2>&1 || true
  deadline=$((SECONDS + 25))
  while kill -0 "$pid" 2>/dev/null && ((SECONDS < deadline)); do
    sleep 0.2
  done
  if kill -0 "$pid" 2>/dev/null; then
    kill -KILL "$pid" >/dev/null 2>&1 || true
    printf '%s\tshutdown_timeout\n' "$current_run" >>"$violations"
  fi
  wait "$pid" 2>/dev/null || true
}

while ((current_run < runs)); do
  current_run=$((current_run + 1))
  : >"$scratch/app.stdout"
  : >"$scratch/app.stderr"
  env -i \
    HOME="$private_home" \
    USER="${USER:-}" \
    LOGNAME="${LOGNAME:-}" \
    LANG="${LANG:-C.UTF-8}" \
    PATH="/usr/bin:/bin:/usr/sbin:/sbin" \
    TMPDIR="${TMPDIR:-/tmp}" \
    FAIRY_RUNTIME_PROFILE="full" \
    FAIRY_DATABASE_URL="postgres://preflight.invalid/fairy" \
    FAIRY_OPENSERP_URL="http://127.0.0.1:9" \
    OPENAI_API_KEY="preflight-not-a-secret" \
    OPENAI_BASE_URL="http://127.0.0.1:9/v1" \
    CODEBUDDY_API_KEY="preflight-not-a-secret" \
    CODEBUDDY_BASE_URL="http://127.0.0.1:9" \
    HTTP_PROXY="http://127.0.0.1:9" \
    HTTPS_PROXY="http://127.0.0.1:9" \
    ALL_PROXY="http://127.0.0.1:9" \
    NO_PROXY="" \
    TELEMETRY_ENABLED="true" \
    "$executable" >"$scratch/app.stdout" 2>"$scratch/app.stderr" &
  app_pid=$!
  printf 'run_%s_pid\t%s\n' "$current_run" "$app_pid" >>"$metadata"

  deadline=$((SECONDS + duration))
  while ((SECONDS < deadline)); do
    if ! kill -0 "$app_pid" 2>/dev/null; then
      printf '%s\tunexpected_app_exit\n' "$current_run" >>"$violations"
      break
    fi
    record_children "$current_run" "$app_pid"
    record_network "$current_run" "$app_pid"
    record_dylibs "$current_run" "$app_pid"
    sleep 0.5
  done
  if kill -0 "$app_pid" 2>/dev/null; then
    quit_app "$app_pid"
  else
    wait "$app_pid" 2>/dev/null || true
  fi
  app_pid=""
done

profile_root="$private_home/Library/Application Support/dev.rinai.fairy/session-core/v1"
[[ -f "$profile_root/runtime.write.lock" && -d "$profile_root/seekdb/store" ]] || printf 'all\tendpoint_profile_not_persisted\n' >>"$violations"
printf 'profile_reused_across_runs\ttrue\n' >>"$metadata"

for evidence in "$children" "$listeners" "$network" "$dylibs" "$violations"; do
  header="$(sed -n '1p' "$evidence")"
  sed '1d' "$evidence" | sort -u >"$scratch/sorted"
  printf '%s\n' "$header" >"$evidence"
  cat "$scratch/sorted" >>"$evidence"
done

if [[ "$(sed -n '$=' "$children")" -gt 1 ]]; then
  printf 'all\tchild_process_observed\n' >>"$violations"
fi

if [[ "$(sed -n '$=' "$violations")" -gt 1 ]]; then
  die "runtime boundary violations were recorded in the preflight evidence directory"
fi

metadata_set result preflight_pass
echo "FAIRY package preflight: PASS ($output)"
