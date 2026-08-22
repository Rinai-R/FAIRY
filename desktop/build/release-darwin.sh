#!/bin/bash

# Sign, notarize, staple, and verify an already assembled FAIRY.app. There is
# intentionally no ad-hoc or skip mode: a release without Apple credentials is
# incomplete and must fail closed.

set -euo pipefail

usage() {
  cat <<'EOF'
Usage: release-darwin.sh --app PATH --identity NAME --notary-profile NAME --output PATH

The notary profile must already exist in the login keychain, for example via:
  xcrun notarytool store-credentials PROFILE ...
EOF
}

die() {
  echo "FAIRY release: $*" >&2
  exit 1
}

app=""
identity=""
notary_profile=""
output=""
while (($# > 0)); do
  case "$1" in
    --app)
      (($# >= 2)) || die "--app requires a path"
      app="$2"
      shift 2
      ;;
    --identity)
      (($# >= 2)) || die "--identity requires a value"
      identity="$2"
      shift 2
      ;;
    --notary-profile)
      (($# >= 2)) || die "--notary-profile requires a value"
      notary_profile="$2"
      shift 2
      ;;
    --output)
      (($# >= 2)) || die "--output requires a path"
      output="$2"
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

[[ -n "$app" ]] || die "--app is required"
[[ -n "$identity" && "$identity" == "${identity# }" && "$identity" == "${identity% }" ]] || die "--identity must be a clean non-empty value"
[[ -n "$notary_profile" && "$notary_profile" == "${notary_profile# }" && "$notary_profile" == "${notary_profile% }" ]] || die "--notary-profile must be a clean non-empty value"
[[ -n "$output" ]] || die "--output is required"
[[ -d "$app/Contents" ]] || die "--app must name an assembled .app bundle"
[[ "${app##*.}" == "app" ]] || die "--app must end in .app"
[[ ! -e "$output" ]] || die "--output already exists: $output"
[[ -d "$(dirname "$output")" ]] || die "--output parent directory does not exist"

for tool in codesign ditto plutil shasum spctl xcrun; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool is required"
done

library="$app/Contents/Frameworks/libseekdb.dylib"
executable="$app/Contents/MacOS/FAIRY"
runtime_verifier="$(dirname "$0")/verify-seekdb-runtime-darwin.sh"
[[ -f "$library" && ! -L "$library" ]] || die "packaged libseekdb.dylib is missing or symlinked"
[[ -x "$executable" && ! -L "$executable" ]] || die "packaged FAIRY executable is missing, non-executable, or symlinked"
[[ -x "$runtime_verifier" && ! -L "$runtime_verifier" ]] || die "packaged SeekDB runtime verifier is missing, non-executable, or symlinked"

# Fail before using signing or notarization credentials when an assembled App
# has static package drift or its in-process SeekDB cannot return control to
# the host. The same gates are repeated after signing and staple below.
"$executable" --verify-package-layout
"$runtime_verifier" --app "$app"

# Nested code is signed first. The enclosing App signature then seals the
# signed dylib and all resource manifests/licenses.
codesign --force --options runtime --timestamp --sign "$identity" "$library"
codesign --force --options runtime --timestamp --sign "$identity" "$app"
codesign --verify --deep --strict --verbose=2 "$app"

notary_dir="$(mktemp -d "${TMPDIR:-/tmp}/fairy-notary.XXXXXX")"
submission="$notary_dir/FAIRY-notary.zip"
cleanup() {
  rm -f "$submission"
  rmdir "$notary_dir" 2>/dev/null || true
}
trap cleanup EXIT

ditto -c -k --keepParent "$app" "$submission"
notary_json="$(xcrun notarytool submit "$submission" --keychain-profile "$notary_profile" --wait --output-format json)"
notary_status="$(printf '%s' "$notary_json" | plutil -extract status raw -o - -)"
[[ "$notary_status" == "Accepted" ]] || die "notarization status is $notary_status, expected Accepted"

xcrun stapler staple "$app"
xcrun stapler validate "$app"
codesign --verify --deep --strict --verbose=2 "$app"
spctl --assess --type execute --verbose=4 "$app"

# Execute the verifier from the final stapled bundle. This mode does not start
# Wails, a model provider, OpenSERP, or SeekDB; it proves the sealed local
# layout, verified SeekDB catalog/native contract, and in-bundle runtime path.
"$executable" --verify-package-layout
"$runtime_verifier" --app "$app"

ditto -c -k --keepParent "$app" "$output"
shasum -a 256 "$output"
echo "FAIRY release: created $output"
