#!/bin/bash

# Reproducible FAIRY libseekdb build recipe for the supported darwin/arm64
# release target. This is a build-time tool only; the shipped application does
# not invoke it or require any of these tools at runtime.

set -euo pipefail

readonly SOURCE_COMMIT="8c2bd7064084d985e3a9c5b8368976ffef8e8394"
readonly CLT_PACKAGE_ID="com.apple.pkg.CLTools_Executables"
readonly CLT_PACKAGE_VERSION="26.6.0.0.1781586589"
readonly SDK_VERSION="26.5"
readonly LLVM_VERSION="19.1.7"
readonly DEPLOYMENT_TARGET="15.0"
readonly CMAKE_VERSION="4.3.2"
readonly RUST_VERSION="1.97.1"
readonly PYTHON_VERSION="3.14.6"
readonly DEFAULT_DEVELOPER_DIR="/Library/Developer/CommandLineTools"
readonly DEFAULT_LLVM_ROOT="/opt/homebrew/opt/llvm@19"

usage() {
  cat <<'EOF'
Usage: darwin-arm64.sh --source PATH --output PATH [--jobs N]

Builds an unsigned, stripped libseekdb.dylib from the exact OceanBase seekdb
source commit recorded by FAIRY. PATH passed to --source must be a clean Git
checkout whose HEAD is the pinned commit. PATH passed to --output must not
already exist.

Required build environment:
  macOS arm64
  Apple Command Line Tools package 26.6.0.0.1781586589, macOS SDK 26.5
  LLVM/Clang 19.1.7 (set FAIRY_LLVM_ROOT if it is not at the Homebrew opt path)
  CMake 4.3.2
  Rust 1.97.1 (also pinned by seekdb/rust/rust-toolchain.toml)
  Python 3.14.6 (set FAIRY_PYTHON3 to its absolute executable path)

The release deployment target is macOS 15.0, matching FAIRY.app's
LSMinimumSystemVersion. Build dependencies are allowed only in this release
recipe; none are shipped or required by the endpoint runtime.
EOF
}

die() {
  echo "seekdb recipe: $*" >&2
  exit 1
}

source_dir=""
output_dir=""
jobs=""
while (($# > 0)); do
  case "$1" in
    --source)
      (($# >= 2)) || die "--source requires a path"
      source_dir="$2"
      shift 2
      ;;
    --output)
      (($# >= 2)) || die "--output requires a path"
      output_dir="$2"
      shift 2
      ;;
    --jobs)
      (($# >= 2)) || die "--jobs requires a positive integer"
      jobs="$2"
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

[[ -n "$source_dir" ]] || die "--source is required"
[[ -n "$output_dir" ]] || die "--output is required"
[[ "$source_dir" == /* ]] || die "--source must be an absolute path"
[[ "$output_dir" == /* ]] || die "--output must be an absolute path"
[[ -d "$source_dir/.git" || -f "$source_dir/.git" ]] || die "--source must be a Git checkout"
[[ ! -e "$output_dir" ]] || die "--output must not already exist"

if [[ -z "$jobs" ]]; then
  jobs="$(sysctl -n hw.logicalcpu)"
fi
[[ "$jobs" =~ ^[1-9][0-9]*$ ]] || die "--jobs must be a positive integer"

[[ "$(uname -s)" == "Darwin" ]] || die "the pinned recipe requires macOS"
[[ "$(uname -m)" == "arm64" ]] || die "the pinned recipe requires an arm64 host"

source_head="$(git -C "$source_dir" rev-parse --verify HEAD)"
[[ "$source_head" == "$SOURCE_COMMIT" ]] || die "source HEAD is $source_head, expected $SOURCE_COMMIT"
[[ -z "$(git -C "$source_dir" status --porcelain=v1 --untracked-files=all)" ]] || die "source checkout must be clean"

export DEVELOPER_DIR="${DEVELOPER_DIR:-$DEFAULT_DEVELOPER_DIR}"
[[ "$DEVELOPER_DIR" == "$DEFAULT_DEVELOPER_DIR" ]] || die "DEVELOPER_DIR must be $DEFAULT_DEVELOPER_DIR"
[[ -d "$DEVELOPER_DIR" ]] || die "Apple Command Line Tools are required at $DEVELOPER_DIR"

clt_package_version="$(pkgutil --pkg-info "$CLT_PACKAGE_ID" | sed -n 's/^version: //p')"
[[ "$clt_package_version" == "$CLT_PACKAGE_VERSION" ]] || die "Command Line Tools package is ${clt_package_version:-missing}, expected $CLT_PACKAGE_VERSION"

sdk_version="$(xcrun --sdk macosx --show-sdk-version)"
[[ "$sdk_version" == "$SDK_VERSION" ]] || die "macOS SDK is $sdk_version, expected $SDK_VERSION"
sdk_root="$(xcrun --sdk macosx --show-sdk-path)"

llvm_root="${FAIRY_LLVM_ROOT:-$DEFAULT_LLVM_ROOT}"
[[ "$llvm_root" == /* ]] || die "FAIRY_LLVM_ROOT must be an absolute path"
cc="$llvm_root/bin/clang"
cxx="$llvm_root/bin/clang++"
[[ -x "$cc" && -x "$cxx" ]] || die "LLVM/Clang is required at $llvm_root"
llvm_version="$("$cc" --version | sed -n '1s/^Homebrew clang version //p')"
[[ "$llvm_version" == "$LLVM_VERSION" ]] || die "LLVM/Clang is ${llvm_version:-unknown}, expected $LLVM_VERSION"
[[ "$("$cc" -dumpmachine)" == arm64-apple-darwin* ]] || die "LLVM/Clang must target arm64-apple-darwin"

cmake_version="$(cmake --version | sed -n '1s/^cmake version //p')"
[[ "$cmake_version" == "$CMAKE_VERSION" ]] || die "CMake is $cmake_version, expected $CMAKE_VERSION"
rust_version="$(rustc --version | awk '{print $2}')"
[[ "$rust_version" == "$RUST_VERSION" ]] || die "Rust is $rust_version, expected $RUST_VERSION"
python_path="${FAIRY_PYTHON3:-}"
[[ -n "$python_path" ]] || die "FAIRY_PYTHON3 must name the absolute Python $PYTHON_VERSION executable"
[[ "$python_path" == /* && -x "$python_path" ]] || die "FAIRY_PYTHON3 must name an executable absolute path"
python_version="$("$python_path" -c 'import platform; print(platform.python_version())')"
[[ "$python_version" == "$PYTHON_VERSION" ]] || die "Python is $python_version, expected $PYTHON_VERSION"

pinned_rust="$(sed -n 's/^channel = "\([^"]*\)"/\1/p' "$source_dir/rust/rust-toolchain.toml" | head -n 1)"
[[ "$pinned_rust" == "$RUST_VERSION" ]] || die "source pins Rust $pinned_rust, expected $RUST_VERSION"

# Reject a stale configured build tree. A release input must be generated from
# the clean pinned checkout by this invocation, never inherited from another
# SDK, deployment target, or source path.
[[ ! -e "$source_dir/build_release" ]] || die "source build_release already exists; use a fresh clean checkout"

export LC_ALL=C
export LANG=C
export TZ=UTC
export ZERO_AR_DATE=1
export SOURCE_DATE_EPOCH="$(git -C "$source_dir" show -s --format=%ct "$SOURCE_COMMIT")"
export MACOSX_DEPLOYMENT_TARGET="$DEPLOYMENT_TARGET"
export CMAKE_OSX_DEPLOYMENT_TARGET="$DEPLOYMENT_TARGET"
export CMAKE_OSX_ARCHITECTURES=arm64
export CC="$cc"
export CXX="$cxx"
export PATH="$(dirname "$python_path"):$PATH"

(
  cd "$source_dir"
  bash build.sh init
  bash build.sh release \
    -DOB_CC="$cc" \
    -DOB_CXX="$cxx" \
    -DOB_USE_CCACHE=OFF \
    -DBUILD_EMBED_MODE=ON \
    -DPython3_EXECUTABLE="$python_path" \
    -DCMAKE_OSX_ARCHITECTURES=arm64 \
    -DCMAKE_OSX_DEPLOYMENT_TARGET="$DEPLOYMENT_TARGET" \
    -DCMAKE_OSX_SYSROOT="$sdk_root" \
    -DCMAKE_SKIP_RPATH=ON \
    -DCMAKE_C_FLAGS_RELWITHDEBINFO="-O2 -g -DNDEBUG -Wno-error=unknown-warning-option" \
    -DCMAKE_CXX_FLAGS_RELWITHDEBINFO="-O2 -g -DNDEBUG -Wno-error=unknown-warning-option -Wno-error=cast-function-type-mismatch -Wno-error=explicit-specialization-storage-class" \
    -DENABLE_AUTO_FDO=OFF \
    -DENABLE_THIN_LTO=OFF \
    -DENABLE_HOTFUNC=OFF \
    --make libseekdb -j "$jobs"
)

library="$source_dir/build_release/src/include/libseekdb.dylib"
[[ -f "$library" ]] || die "build did not produce $library"

mkdir "$output_dir"
cp "$library" "$output_dir/libseekdb.dylib"
cp "$source_dir/LICENSE" "$output_dir/LICENSE"
cp "$source_dir/NOTICE" "$output_dir/NOTICE"

# The catalog verifies a stripped, unsigned build input. Release packaging
# signs this nested dylib before signing the enclosing app.
/usr/bin/strip -Sx "$output_dir/libseekdb.dylib"
if codesign -d "$output_dir/libseekdb.dylib" >/dev/null 2>&1; then
  codesign --remove-signature "$output_dir/libseekdb.dylib"
fi

cat >"$output_dir/build-evidence.txt" <<EOF
source_commit=$SOURCE_COMMIT
command_line_tools_package_id=$CLT_PACKAGE_ID
command_line_tools_package_version=$CLT_PACKAGE_VERSION
sdk_version=$SDK_VERSION
sdk_path=$sdk_root
llvm_version=$LLVM_VERSION
llvm_root=$llvm_root
deployment_target=$DEPLOYMENT_TARGET
cmake_version=$CMAKE_VERSION
rust_version=$RUST_VERSION
python_version=$PYTHON_VERSION
architecture=arm64
build_type=RelWithDebInfo
build_target=libseekdb
build_embed_mode=ON
use_ccache=OFF
enable_auto_fdo=OFF
enable_thin_lto=OFF
enable_hotfunc=OFF
cmake_skip_rpath=ON
EOF

shasum -a 256 "$output_dir/libseekdb.dylib" "$output_dir/LICENSE" "$output_dir/NOTICE" >"$output_dir/SHA256SUMS"
echo "seekdb recipe: built $output_dir/libseekdb.dylib"
