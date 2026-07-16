package visual

import (
	"log"
	"net/http"
	"os"
	"strings"
)

// AssetHandler serves local visual-pack PNGs at the Wails service route
// `/fairy-character` (path after route strip is `/<pack>/...png`).
type AssetHandler struct {
	visualPacksRoot string
}

func NewAssetHandler(configRoot string) *AssetHandler {
	return &AssetHandler{visualPacksRoot: VisualPacksRoot(configRoot)}
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
	log.Printf("fairy-character serve %s (%d bytes)", relative, len(bytes))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, _ = w.Write(bytes)
}
