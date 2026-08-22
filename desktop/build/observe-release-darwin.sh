#!/bin/bash

# Observe the runtime boundary of an assembled FAIRY.app.
#
# This is an endpoint QA tool, not an App runtime dependency. It intentionally
# uses only macOS system tools, accepts origins but never credentials, launches
# the packaged executable with a clean environment, and writes sanitized TSV
# evidence. Provider credentials/configuration must already have been saved by
# the user through FAIRY management before this command is run. Developer ID
# and notarization are optional public-distribution checks, not endpoint gates.

set -euo pipefail

usage() {
  cat <<'EOF'
Usage: observe-release-darwin.sh \
  --app PATH \
  --chat-origin URL \
  --embedding-origin URL \
  [--openserp-origin URL] \
  --output DIRECTORY \
  [--duration SECONDS] \
  [--runs COUNT] \
  [--require-public-distribution]

The command never accepts an API key. Chat and embedding provider credentials
must already be stored by FAIRY. OpenSERP is optional; omit its origin when the
saved OpenSERP capability is disabled for the scenario under test.

During each observation window, exercise the intended scenario in the App.
The command then quits and relaunches the same assembled bundle so restart behavior
and the runtime boundary are observed from the actual installed artifact.

By default an ad-hoc signed App is accepted. Pass --require-public-distribution
only when separately validating Developer ID, notarization, staple, and
Gatekeeper for a public macOS distribution.
EOF
}

die() {
  echo "FAIRY endpoint evidence: $*" >&2
  exit 1
}

app=""
chat_origin=""
embedding_origin=""
openserp_origin=""
output=""
duration=30
runs=2
require_public_distribution=false

while (($# > 0)); do
  case "$1" in
    --app)
      (($# >= 2)) || die "--app requires a path"
      app="$2"
      shift 2
      ;;
    --chat-origin)
      (($# >= 2)) || die "--chat-origin requires a URL"
      chat_origin="$2"
      shift 2
      ;;
    --embedding-origin)
      (($# >= 2)) || die "--embedding-origin requires a URL"
      embedding_origin="$2"
      shift 2
      ;;
    --openserp-origin)
      (($# >= 2)) || die "--openserp-origin requires a URL"
      openserp_origin="$2"
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
    --require-public-distribution)
      require_public_distribution=true
      shift
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

[[ "$(uname -s)" == "Darwin" ]] || die "this verifier requires macOS"
[[ -n "$app" && "$app" != *$'\t'* && "$app" != *$'\n'* && -d "$app/Contents" && "${app##*.}" == "app" ]] || die "--app must name an assembled .app bundle"
[[ -n "$chat_origin" ]] || die "--chat-origin is required"
[[ -n "$embedding_origin" ]] || die "--embedding-origin is required"
[[ -n "$output" && "$output" != *$'\t'* && "$output" != *$'\n'* && ! -e "$output" ]] || die "--output must name a new directory"
[[ "$duration" =~ ^[0-9]+$ && "$duration" -ge 5 && "$duration" -le 3600 ]] || die "--duration must be 5..3600 seconds"
[[ "$runs" =~ ^[0-9]+$ && "$runs" -ge 2 && "$runs" -le 10 ]] || die "--runs must be 2..10"

# lsof reports absolute paths. Resolve the caller-supplied bundle path before
# comparing observed files with the packaged SeekDB library so a relative App
# path cannot create a false undeclared-library violation in release evidence.
app_parent="$(cd "$(dirname "$app")" && pwd -P)"
app="$app_parent/${app##*/}"

for tool in awk basename cat codesign dscacheutil env kill lsof mkdir mktemp osascript plutil ps rm rmdir sed shasum sleep sort sw_vers tr uname uniq; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool is required"
done
if [[ "$require_public_distribution" == "true" ]]; then
  for tool in spctl syspolicy_check; do
    command -v "$tool" >/dev/null 2>&1 || die "$tool is required for public distribution verification"
  done
fi

executable="$app/Contents/MacOS/FAIRY"
library="$app/Contents/Frameworks/libseekdb.dylib"
info_plist="$app/Contents/Info.plist"
[[ -x "$executable" && ! -L "$executable" ]] || die "packaged FAIRY executable is missing, non-executable, or symlinked"
[[ -f "$library" && ! -L "$library" ]] || die "packaged libseekdb.dylib is missing or symlinked"
[[ -f "$info_plist" && ! -L "$info_plist" ]] || die "packaged Info.plist is missing or symlinked"

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
scratch="$(mktemp -d "${TMPDIR:-/tmp}/fairy-runtime-evidence.XXXXXX")"
app_pid=""

cleanup() {
  if [[ -n "$app_pid" ]] && kill -0 "$app_pid" 2>/dev/null; then
    osascript -e "tell application id \"$bundle_id\" to quit" >/dev/null 2>&1 || true
    kill -TERM "$app_pid" >/dev/null 2>&1 || true
  fi
  rm -f "$scratch"/* 2>/dev/null || true
  rmdir "$scratch" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

metadata="$output/metadata.tsv"
allowed="$output/allowed-egress.tsv"
children="$output/child-processes.tsv"
listeners="$output/listeners.tsv"
outbound="$output/outbound.tsv"
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
printf 'schema_version\t3\n' >>"$metadata"
printf 'verification_level\tendpoint_attempt\n' >>"$metadata"
printf 'result\tfail\n' >>"$metadata"
printf 'endpoint_eligible\tfalse\n' >>"$metadata"
printf 'public_distribution_required\t%s\n' "$require_public_distribution" >>"$metadata"
printf 'release_eligible\tfalse\n' >>"$metadata"
printf 'package_verification\tfail\n' >>"$metadata"
printf 'provider_configuration_checked\tfalse\n' >>"$metadata"
printf 'host_platform_supported\tfalse\n' >>"$metadata"
printf 'developer_id_checked\tfalse\n' >>"$metadata"
printf 'notarization_checked\tfalse\n' >>"$metadata"
printf 'staple_checked\tfalse\n' >>"$metadata"
printf 'distribution_policy_checked\tfalse\n' >>"$metadata"
printf 'gatekeeper_checked\tfalse\n' >>"$metadata"
# This process/socket observer proves the declared egress boundary. It cannot
# prove HTTP-level chat or embedding behavior, especially when both capabilities
# share one provider origin; those smokes remain separate integration evidence.
printf 'provider_smoke_checked\tfalse\n' >>"$metadata"
printf 'provider_egress_boundary_checked\tfalse\n' >>"$metadata"
printf 'egress_attribution\tdeclared_origin_set\n' >>"$metadata"
printf 'capability_origin_overlap\tfalse\n' >>"$metadata"
printf 'bundle_identifier\t%s\n' "$bundle_id" >>"$metadata"
printf 'host_os_version\t%s\n' "$host_os_version" >>"$metadata"
printf 'host_arch\t%s\n' "$host_arch" >>"$metadata"
printf 'app_minimum_system_version\t%s\n' "$app_minimum_system_version" >>"$metadata"
printf 'profile\tendpoint-strict\n' >>"$metadata"
printf 'duration_seconds\t%s\n' "$duration" >>"$metadata"
printf 'runs\t%s\n' "$runs" >>"$metadata"
printf 'seekdb_telemetry_override_attempt\ttrue\n' >>"$metadata"
printf 'executable_sha256\t%s\n' "$(shasum -a 256 "$executable" | awk '{print $1}')" >>"$metadata"
printf 'seekdb_sha256\t%s\n' "$(shasum -a 256 "$library" | awk '{print $1}')" >>"$metadata"
printf 'run\tpid\tppid\texecutable\n' >"$children"
printf 'run\tprotocol\tlocal_endpoint\n' >"$listeners"
printf 'run\tcapability\tprotocol\tremote_endpoint\n' >"$outbound"
printf 'run\tpath\n' >"$dylibs"
printf 'run\tcode\n' >"$violations"
printf 'capability\torigin\tresolved_endpoint\n' >"$allowed"

[[ "$host_arch" == "arm64" ]] || die "host architecture $host_arch is unsupported; want arm64"
version_at_least "$host_os_version" "$app_minimum_system_version" || die "host macOS $host_os_version is older than App minimum $app_minimum_system_version"
metadata_set host_platform_supported true

provider_ip_forbidden() {
  local ip
  ip="$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')"
  case "$ip" in
    0.0.0.0|127.*|169.254.*|22[4-9].*|23[0-9].*|255.255.255.255|\
    ::|::1|0:0:0:0:0:0:0:0|0:0:0:0:0:0:0:1|fe8*|fe9*|fea*|feb*|ff*|\
    ::ffff:0.0.0.0|::ffff:127.*|::ffff:169.254.*|::ffff:22[4-9].*|::ffff:23[0-9].*|\
    0:0:0:0:0:ffff:0.0.0.0|0:0:0:0:0:ffff:127.*|0:0:0:0:0:ffff:169.254.*|0:0:0:0:0:ffff:22[4-9].*|0:0:0:0:0:ffff:23[0-9].*)
      return 0
      ;;
  esac
  return 1
}

normalize_origin() {
  local capability="$1"
  local raw="$2"
  local scheme authority host port default_port line ip endpoint normalized

  [[ "$raw" == "${raw# }" && "$raw" == "${raw% }" ]] || die "$capability origin must not contain surrounding whitespace"
  case "$raw" in
    http://*)
      scheme="http"
      authority="${raw#http://}"
      default_port=80
      ;;
    https://*)
      scheme="https"
      authority="${raw#https://}"
      default_port=443
      ;;
    *)
      die "$capability origin must use http or https"
      ;;
  esac
  [[ -n "$authority" && "$authority" != *'/'* && "$authority" != *'?'* && "$authority" != *'#'* && "$authority" != *'@'* ]] || die "$capability origin must be an origin without path, query, fragment, or userinfo"

  if [[ "$authority" == \[* ]]; then
    [[ "$authority" == *']'* ]] || die "$capability origin has an invalid IPv6 host"
    host="${authority%%]*}"
    host="${host#[}"
    line="${authority#*]}"
    if [[ -n "$line" ]]; then
      [[ "$line" == :* ]] || die "$capability origin has an invalid port"
      port="${line#:}"
    else
      port="$default_port"
    fi
  else
    host="${authority%%:*}"
    line="${authority#"$host"}"
    [[ "$line" != *:*:* ]] || die "$capability IPv6 origin must use brackets"
    if [[ -n "$line" ]]; then
      [[ "$line" == :* ]] || die "$capability origin has an invalid port"
      port="${line#:}"
    else
      port="$default_port"
    fi
  fi
  host="$(printf '%s' "$host" | tr '[:upper:]' '[:lower:]')"
  [[ -n "$host" && "$port" =~ ^[0-9]+$ && "$port" -ge 1 && "$port" -le 65535 ]] || die "$capability origin host or port is invalid"
  if [[ "$host" == *:* ]]; then
    [[ "$host" =~ ^[0-9a-f:.]+$ ]] || die "$capability origin IPv6 host is invalid"
  else
    [[ "$host" =~ ^[a-z0-9.-]+$ && "$host" != .* && "$host" != *. ]] || die "$capability origin host is invalid"
  fi
  if [[ "$capability" != "openserp" ]]; then
    case "$host" in
      localhost|*.localhost)
        die "$capability local provider origin is forbidden"
        ;;
    esac
    if [[ "$host" == *:* || "$host" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
      provider_ip_forbidden "$host" && die "$capability local provider origin is forbidden"
    fi
  fi
  normalized="$scheme://$host"
  if [[ "$port" -ne "$default_port" ]]; then
    if [[ "$host" == *:* ]]; then
      normalized="$scheme://[$host]:$port"
    else
      normalized="$scheme://$host:$port"
    fi
  elif [[ "$host" == *:* ]]; then
    normalized="$scheme://[$host]"
  fi

  : >"$scratch/$capability.ips"
  if [[ "$host" == *:* || "$host" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    printf '%s\n' "$host" >"$scratch/$capability.ips"
  else
    dscacheutil -q host -a name "$host" | awk '/^ip_address: / {print $2}' | sort -u >"$scratch/$capability.ips"
  fi
  [[ -s "$scratch/$capability.ips" ]] || die "$capability origin did not resolve"
  while IFS= read -r ip; do
    ip="$(printf '%s' "$ip" | tr '[:upper:]' '[:lower:]')"
    [[ -n "$ip" && "$ip" != *$'\t'* && "$ip" != *$'\n'* ]] || die "$capability origin resolved to an invalid address"
    if [[ "$capability" != "openserp" ]]; then
      provider_ip_forbidden "$ip" && die "$capability origin resolved to a local provider address"
    fi
    if [[ "$ip" == *:* ]]; then
      endpoint="[$ip]:$port"
    else
      endpoint="$ip:$port"
    fi
    printf '%s\t%s\t%s\n' "$capability" "$normalized" "$endpoint" >>"$allowed"
  done <"$scratch/$capability.ips"
}

normalize_origin chat "$chat_origin"
normalize_origin embedding "$embedding_origin"
if [[ -n "$openserp_origin" ]]; then
  normalize_origin openserp "$openserp_origin"
fi

# A TCP socket cannot reveal which HTTP capability used it. Product settings
# may legitimately point multiple capabilities at one provider, so record that
# overlap explicitly and evaluate this artifact as declared-origin evidence.
# HTTP-level chat/embedding behavior is proved by separate live integration.
if awk -F '\t' '
  NR > 1 {
    pair = $1 SUBSEP $3
    if (!seen[pair]++) endpoint_capabilities[$3]++
  }
  END {
    for (endpoint in endpoint_capabilities) {
      if (endpoint_capabilities[endpoint] > 1) exit 0
    }
    exit 1
  }
' "$allowed"; then
  metadata_set capability_origin_overlap true
fi

# Package gates are repeated here so runtime evidence can never be attached to
# a structurally different App. Ad-hoc code signing is sufficient for endpoint
# acceptance; Apple distribution policy is checked only when explicitly asked.
codesign --verify --deep --strict --verbose=2 "$app" >/dev/null
"$executable" --verify-package-layout
if [[ -n "$openserp_origin" ]]; then
  env -i \
    HOME="$HOME" \
    USER="${USER:-}" \
    LOGNAME="${LOGNAME:-}" \
    LANG="${LANG:-C.UTF-8}" \
    PATH="/usr/bin:/bin:/usr/sbin:/sbin" \
    TMPDIR="${TMPDIR:-/tmp}" \
    "$executable" --verify-endpoint-readiness --require-openserp
else
  env -i \
    HOME="$HOME" \
    USER="${USER:-}" \
    LOGNAME="${LOGNAME:-}" \
    LANG="${LANG:-C.UTF-8}" \
    PATH="/usr/bin:/bin:/usr/sbin:/sbin" \
    TMPDIR="${TMPDIR:-/tmp}" \
    "$executable" --verify-endpoint-readiness
fi
metadata_set provider_configuration_checked true
"$(dirname "$0")/verify-seekdb-runtime-darwin.sh" --app "$app"
metadata_set package_verification pass

if [[ "$require_public_distribution" == "true" ]]; then
  app_signature="$(codesign --display --verbose=4 "$app" 2>&1)"
  library_signature="$(codesign --display --verbose=4 "$library" 2>&1)"
  app_team_id="$(printf '%s\n' "$app_signature" | sed -n 's/^TeamIdentifier=//p' | sed -n '1p')"
  library_team_id="$(printf '%s\n' "$library_signature" | sed -n 's/^TeamIdentifier=//p' | sed -n '1p')"
  app_authority="$(printf '%s\n' "$app_signature" | sed -n 's/^Authority=//p' | sed -n '1p')"
  [[ -n "$app_team_id" && "$app_team_id" != "not set" ]] || die "App is not signed with a Developer ID team"
  [[ "$app_authority" == "Developer ID Application:"* ]] || die "App is not signed with a Developer ID Application identity"
  [[ "$library_team_id" == "$app_team_id" ]] || die "packaged libseekdb.dylib TeamIdentifier differs from the App"
  metadata_set team_identifier "$app_team_id"
  metadata_set developer_id_checked true
  if ! syspolicy_check distribution "$app" --json >"$scratch/distribution-policy.json" 2>/dev/null; then
    die "system distribution policy rejected the packaged App"
  fi
  metadata_set notarization_checked true
  metadata_set staple_checked true
  metadata_set distribution_policy_checked true
  if ! spctl --assess --type execute --verbose=4 "$app" >"$scratch/gatekeeper.txt" 2>&1; then
    die "Gatekeeper rejected the packaged App"
  fi
  metadata_set gatekeeper_checked true
fi

allowed_capability() {
  local endpoint="$1"
  # Multiple comma-separated labels are intentional when capabilities share a
  # provider endpoint. They describe the declared origin set, not HTTP behavior.
  awk -F '\t' -v endpoint="$endpoint" 'NR > 1 && $3 == endpoint {labels = labels (labels ? "," : "") $1} END {if (labels != "") print labels}' "$allowed"
}

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
  local protocol endpoint state remote capability
  lsof -nP -a -p "$pid" -i 2>/dev/null | awk 'NR > 1 {print $8 "\t" $9 "\t" $10}' >"$scratch/network" || true
  while IFS=$'\t' read -r protocol endpoint state; do
    [[ -n "$protocol" && -n "$endpoint" ]] || continue
    if [[ "$state" == "(LISTEN)" ]]; then
      printf '%s\t%s\t%s\n' "$run" "$protocol" "$endpoint" >>"$listeners"
      printf '%s\tunexpected_listener\n' "$run" >>"$violations"
      continue
    fi
    if [[ "$protocol" != "TCP" ]]; then
      printf '%s\tunexpected_non_tcp_socket\n' "$run" >>"$violations"
      continue
    fi
    if [[ "$endpoint" != *'->'* ]]; then
      printf '%s\tunclassified_tcp_socket\n' "$run" >>"$violations"
      continue
    fi
    remote="${endpoint##*->}"
    capability="$(allowed_capability "$remote")"
    if [[ -z "$capability" ]]; then
      printf '%s\tundeclared_remote_endpoint\n' "$run" >>"$violations"
      capability="undeclared"
    fi
    printf '%s\t%s\t%s\t%s\n' "$run" "$capability" "$protocol" "$remote" >>"$outbound"
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
  # The verifier starts Contents/MacOS/FAIRY directly, so LaunchServices may
  # not deliver the AppleScript quit request. SIGTERM is the normal lifecycle
  # signal for this owned PID and still runs the Wails service shutdown hooks.
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

current_run=0
while ((current_run < runs)); do
  current_run=$((current_run + 1))
  : >"$scratch/app.stdout"
  : >"$scratch/app.stderr"
  env -i \
    HOME="$HOME" \
    USER="${USER:-}" \
    LOGNAME="${LOGNAME:-}" \
    LANG="${LANG:-C.UTF-8}" \
    PATH="/usr/bin:/bin:/usr/sbin:/sbin" \
    TMPDIR="${TMPDIR:-/tmp}" \
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

for evidence in "$children" "$listeners" "$outbound" "$dylibs" "$violations"; do
  header="$(sed -n '1p' "$evidence")"
  sed '1d' "$evidence" | sort -u >"$scratch/sorted"
  printf '%s\n' "$header" >"$evidence"
  cat "$scratch/sorted" >>"$evidence"
done

capability_observed_in_run() {
  local capability="$1"
  local run="$2"
  awk -F '\t' -v capability="$capability" -v run="$run" '
    NR > 1 && $1 == run {
      count = split($2, labels, ",")
      for (label_index = 1; label_index <= count; label_index++) {
        if (labels[label_index] == capability) found = 1
      }
    }
    END { exit found ? 0 : 1 }
  ' "$outbound"
}

for capability in chat embedding; do
  capability_complete=true
  for ((run = 1; run <= runs; run++)); do
    if capability_observed_in_run "$capability" "$run"; then
      metadata_set "${capability}_egress_run_${run}" true
    else
      capability_complete=false
      metadata_set "${capability}_egress_run_${run}" false
      printf '%s\t%s_egress_not_observed\n' "$run" "$capability" >>"$violations"
    fi
  done
  if [[ "$capability_complete" == "true" ]]; then
    metadata_set "${capability}_egress_observed" true
  else
    metadata_set "${capability}_egress_observed" false
  fi
done
if [[ -n "$openserp_origin" ]]; then
  metadata_set openserp_egress_required true
  openserp_complete=true
  for ((run = 1; run <= runs; run++)); do
    if capability_observed_in_run openserp "$run"; then
      metadata_set "openserp_egress_run_${run}" true
    else
      openserp_complete=false
      metadata_set "openserp_egress_run_${run}" false
      printf '%s\topenserp_egress_not_observed\n' "$run" >>"$violations"
    fi
  done
  if [[ "$openserp_complete" == "true" ]]; then
    metadata_set openserp_egress_observed true
  else
    metadata_set openserp_egress_observed false
  fi
else
  metadata_set openserp_egress_required false
  metadata_set openserp_egress_observed false
fi

if [[ "$(sed -n '$=' "$children")" -gt 1 ]]; then
  printf 'all\tchild_process_observed\n' >>"$violations"
fi

if [[ "$(sed -n '$=' "$violations")" -gt 1 ]]; then
  metadata_set provider_smoke_checked false
  metadata_set provider_egress_boundary_checked false
  metadata_set endpoint_eligible false
  metadata_set release_eligible false
  die "runtime boundary violations were recorded in the evidence directory"
fi

metadata_set provider_egress_boundary_checked true
metadata_set endpoint_eligible true
if [[ "$require_public_distribution" == "true" ]]; then
  metadata_set release_eligible true
  metadata_set verification_level final_public_release
else
  metadata_set verification_level final_endpoint
fi
metadata_set result pass
echo "FAIRY endpoint evidence: PASS ($output)"
