#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
REPO_ROOT="$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)"
ASSET_DIR="${FAIRY_BGE_ASSET_DIR:-$REPO_ROOT/fairy/assets/embeddings/bge-small-zh-v1.5}"
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/fairy-bge-assets.XXXXXX")"
trap 'rm -rf "$TMP_DIR"' EXIT

MODEL_URL="https://huggingface.co/onnx-community/bge-small-zh-v1.5-ONNX/resolve/main/onnx/model_quantized.onnx"
MODEL_SHA256="99a6e522710c00220c89f8c52e0cc5aa09d4cbb1c34c0e932eab3a9dfdc65df3"
MODEL_DATA_URL="https://huggingface.co/onnx-community/bge-small-zh-v1.5-ONNX/resolve/main/onnx/model_quantized.onnx_data"
MODEL_DATA_SHA256="952623481ca8beea884e3d3c9ecaf8a3c7bf1d0c21de29e970cd31af9d37a90b"
TOKENIZER_URL="https://huggingface.co/onnx-community/bge-small-zh-v1.5-ONNX/resolve/main/tokenizer.json"
TOKENIZER_SHA256="3d09c84ebd10306706a79a8276b3ab736a40d8ec03251c7639f4e52c3a1a4f8e"

sha256_file() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
    return
  fi
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
    return
  fi
  echo "missing shasum or sha256sum" >&2
  exit 1
}

verify_sha256() {
  local path="$1"
  local want="$2"
  local got
  got="$(sha256_file "$path")"
  if [[ "$got" != "$want" ]]; then
    echo "sha256 mismatch for $path" >&2
    echo "want: $want" >&2
    echo " got: $got" >&2
    exit 1
  fi
}

install_url() {
  local url="$1"
  local want_sha="$2"
  local dest="$3"
  if [[ -f "$dest" ]]; then
    if [[ "$(sha256_file "$dest")" == "$want_sha" ]]; then
      echo "ready: $dest"
      return
    fi
    echo "replacing asset with unexpected checksum: $dest"
  fi
  local tmp="$TMP_DIR/$(basename "$dest").download"
  curl -L --fail --retry 3 --connect-timeout 30 --output "$tmp" "$url"
  verify_sha256 "$tmp" "$want_sha"
  mkdir -p "$(dirname "$dest")"
  mv "$tmp" "$dest"
  chmod 0644 "$dest"
  echo "installed: $dest"
}

runtime_asset() {
  local platform="${FAIRY_BGE_RUNTIME_PLATFORM:-$(uname -s)-$(uname -m)}"
  case "$platform" in
    Darwin-arm64)
      RUNTIME_ARCHIVE_URL="https://github.com/microsoft/onnxruntime/releases/download/v1.26.0/onnxruntime-osx-arm64-1.26.0.tgz"
      RUNTIME_ARCHIVE_SHA256="7a1280bbb1701ea514f71828765237e7896e0f2e1cd332f1f70dbd5c3e33aca3"
      RUNTIME_ARCHIVE_NAME="onnxruntime-osx-arm64-1.26.0.tgz"
      RUNTIME_EXTRACTED_FILE="onnxruntime-osx-arm64-1.26.0/lib/libonnxruntime.dylib"
      RUNTIME_DEST_NAME="libonnxruntime.dylib"
      ;;
    *)
      echo "unsupported local BGE runtime platform: $platform" >&2
      echo "Currently packaged by this script: Darwin-arm64." >&2
      exit 1
      ;;
  esac
}

install_runtime() {
  runtime_asset
  local dest="$ASSET_DIR/$RUNTIME_DEST_NAME"
  if [[ -f "$dest" ]]; then
    echo "ready: $dest"
    return
  fi
  local archive="$TMP_DIR/$RUNTIME_ARCHIVE_NAME"
  curl -L --fail --retry 3 --connect-timeout 30 --output "$archive" "$RUNTIME_ARCHIVE_URL"
  verify_sha256 "$archive" "$RUNTIME_ARCHIVE_SHA256"
  tar -xzf "$archive" -C "$TMP_DIR"
  local extracted="$TMP_DIR/$RUNTIME_EXTRACTED_FILE"
  if [[ ! -f "$extracted" ]]; then
    echo "ONNX Runtime archive missing expected file: $RUNTIME_EXTRACTED_FILE" >&2
    exit 1
  fi
  mkdir -p "$ASSET_DIR"
  cp "$extracted" "$dest"
  chmod 0644 "$dest"
  echo "installed: $dest"
}

mkdir -p "$ASSET_DIR"
install_url "$MODEL_URL" "$MODEL_SHA256" "$ASSET_DIR/model.onnx"
install_url "$MODEL_DATA_URL" "$MODEL_DATA_SHA256" "$ASSET_DIR/model_quantized.onnx_data"
install_url "$TOKENIZER_URL" "$TOKENIZER_SHA256" "$ASSET_DIR/tokenizer.json"
install_runtime

cat > "$ASSET_DIR/SOURCE_MANIFEST.txt" <<EOF
bge-small-zh-v1.5 ONNX assets
model: onnx-community/bge-small-zh-v1.5-ONNX onnx/model_quantized.onnx
model data: onnx-community/bge-small-zh-v1.5-ONNX onnx/model_quantized.onnx_data
tokenizer: onnx-community/bge-small-zh-v1.5-ONNX tokenizer.json
onnx runtime: microsoft/onnxruntime v1.26.0
prepared_at_unix: $(date +%s)
EOF
chmod 0644 "$ASSET_DIR/SOURCE_MANIFEST.txt"
echo "local BGE assets are ready in $ASSET_DIR"
