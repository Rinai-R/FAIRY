package runtime

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Rinai-R/FAIRY/internal/app"
)

const (
	DefaultMaterialDir       = "data/materials"
	MaxDocumentAssetBytes    = 64 << 20
	defaultDocumentAssetName = "material"
)

var unsafeFilenameChars = regexp.MustCompile(`[^a-zA-Z0-9._\-\p{Han}]`)

func (r *Runtime) StoreDocumentAsset(_ context.Context, req app.DocumentUploadRequest) (app.DocumentAsset, error) {
	if strings.TrimSpace(req.Filename) == "" {
		return app.DocumentAsset{}, errors.New("filename 不能为空")
	}
	if req.DataBase64 == "" {
		return app.DocumentAsset{}, errors.New("data_base64 不能为空")
	}
	data, err := base64.StdEncoding.DecodeString(req.DataBase64)
	if err != nil {
		return app.DocumentAsset{}, fmt.Errorf("data_base64 不是有效 base64: %w", err)
	}
	return r.storeDocumentAsset(req.Filename, req.ContentType, data)
}

func (r *Runtime) StoreDocumentAssetBytes(_ context.Context, filename string, contentType string, data []byte) (app.DocumentAsset, error) {
	return r.storeDocumentAsset(filename, contentType, data)
}

func (r *Runtime) storeDocumentAsset(filename string, contentType string, data []byte) (app.DocumentAsset, error) {
	if strings.TrimSpace(filename) == "" {
		return app.DocumentAsset{}, errors.New("filename 不能为空")
	}
	if len(data) == 0 {
		return app.DocumentAsset{}, errors.New("文件内容不能为空")
	}
	if int64(len(data)) > MaxDocumentAssetBytes {
		return app.DocumentAsset{}, fmt.Errorf("文件过大: %d bytes，当前上限 %d bytes", len(data), MaxDocumentAssetBytes)
	}

	id := time.Now().UTC().Format("20060102150405.000000000")
	safeName := safeFilename(filename)
	name := id + "-" + safeName
	materialDir := r.materialDir
	if materialDir == "" {
		materialDir = DefaultMaterialDir
	}
	if err := os.MkdirAll(materialDir, 0o755); err != nil {
		return app.DocumentAsset{}, fmt.Errorf("创建材料目录失败: %w", err)
	}
	target := filepath.Join(materialDir, name)
	if err := os.WriteFile(target, data, 0o600); err != nil {
		return app.DocumentAsset{}, fmt.Errorf("保存材料文件失败: %w", err)
	}
	return app.DocumentAsset{
		ID:          id,
		Filename:    safeName,
		ContentType: contentType,
		Path:        target,
		SizeBytes:   int64(len(data)),
	}, nil
}

func safeFilename(value string) string {
	base := filepath.Base(strings.TrimSpace(value))
	base = unsafeFilenameChars.ReplaceAllString(base, "_")
	base = strings.Trim(base, "._- ")
	if base == "" {
		return defaultDocumentAssetName
	}
	return base
}
