package visual

import (
	"net/http"
	"os"
	"strings"

	"go.uber.org/zap"
)

// AssetHandler serves local visual-pack PNGs at the Wails service route
// `/fairy-character` (path after route strip is `/<pack>/...png`).
type AssetHandler struct {
	visualPacksRoot string
	logger          *zap.Logger
}

func NewAssetHandler(configRoot string) *AssetHandler {
	return &AssetHandler{visualPacksRoot: VisualPacksRoot(configRoot), logger: zap.NewNop()}
}

// AttachLogger injects the process logger (dependency injection, no global).
func AttachLogger(h *AssetHandler, logger *zap.Logger) {
	if h == nil || logger == nil {
		return
	}
	h.logger = logger
}

func (h *AssetHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	relative := strings.TrimPrefix(r.URL.Path, "/")
	full, err := ResolveAssetFile(h.visualPacksRoot, relative)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, "character asset not found", http.StatusNotFound)
			return
		}
		http.Error(w, "invalid character asset path", http.StatusBadRequest)
		return
	}
	bytes, err := os.ReadFile(full)
	if err != nil {
		http.Error(w, "character asset not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	h.logger.Debug("fairy-character serve", zap.String("path", relative), zap.Int("bytes", len(bytes)))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, _ = w.Write(bytes)
}
