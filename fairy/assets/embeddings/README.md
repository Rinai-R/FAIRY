# Local embedding assets

Place release-bundled local embedding assets under:

- assets/embeddings/bge-small-zh-v1.5/model.onnx
- assets/embeddings/bge-small-zh-v1.5/model_quantized.onnx_data
- assets/embeddings/bge-small-zh-v1.5/tokenizer.json
- assets/embeddings/bge-small-zh-v1.5/libonnxruntime.dylib (macOS)
- assets/embeddings/bge-small-zh-v1.5/libonnxruntime.so (Linux)
- assets/embeddings/bge-small-zh-v1.5/onnxruntime.dll (Windows)

The app copies those files into the user's config root on startup when they are
present. The repository intentionally does not include the large model/runtime
assets.

Use scripts/prepare-local-bge-assets.sh before release builds to download the
pinned quantized BGE ONNX model, its external data shard, tokenizer, and the
matching ONNX Runtime library for the packaging platform. FAIRY does not start a
local BGE sidecar and does not download these assets at application runtime; the
local provider loads them in-process on demand.
