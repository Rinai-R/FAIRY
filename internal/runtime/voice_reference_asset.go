package runtime

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Rinai-R/FAIRY/internal/app"
)

const (
	DefaultVoiceReferenceDirName = "voice-references"
	MaxVoiceReferenceAudioBytes  = 64 << 20
	defaultVoiceReferenceName    = "voice-reference.wav"
)

func (r *Runtime) StoreVoiceReferenceAudio(_ context.Context, req app.DocumentUploadRequest) (app.DocumentAsset, error) {
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
	return r.StoreVoiceReferenceAudioBytes(req.Filename, req.ContentType, data)
}

func (r *Runtime) StoreVoiceReferenceAudioBytes(filename string, contentType string, data []byte) (app.DocumentAsset, error) {
	if strings.TrimSpace(filename) == "" {
		return app.DocumentAsset{}, errors.New("filename 不能为空")
	}
	if len(data) == 0 {
		return app.DocumentAsset{}, errors.New("文件内容不能为空")
	}
	if int64(len(data)) > MaxVoiceReferenceAudioBytes {
		return app.DocumentAsset{}, fmt.Errorf("文件过大: %d bytes，当前上限 %d bytes", len(data), MaxVoiceReferenceAudioBytes)
	}
	if !looksLikeAudio(filename, contentType) {
		return app.DocumentAsset{}, fmt.Errorf("参考音频必须是音频文件: %s", filename)
	}

	id := time.Now().UTC().Format("20060102150405.000000000")
	safeName := safeFilename(filename)
	if safeName == defaultDocumentAssetName {
		safeName = defaultVoiceReferenceName
	}
	materialDir := r.materialDir
	if materialDir == "" {
		materialDir = DefaultMaterialDir
	}
	absoluteMaterialDir, err := filepath.Abs(materialDir)
	if err != nil {
		return app.DocumentAsset{}, fmt.Errorf("解析参考音频目录失败: %w", err)
	}
	targetDir := filepath.Join(absoluteMaterialDir, DefaultVoiceReferenceDirName)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return app.DocumentAsset{}, fmt.Errorf("创建参考音频目录失败: %w", err)
	}
	target := filepath.Join(targetDir, id+"-"+safeName)
	if err := os.WriteFile(target, data, 0o600); err != nil {
		return app.DocumentAsset{}, fmt.Errorf("保存参考音频失败: %w", err)
	}
	return app.DocumentAsset{
		ID:          id,
		Filename:    safeName,
		ContentType: contentType,
		Path:        target,
		SizeBytes:   int64(len(data)),
	}, nil
}

func looksLikeAudio(filename string, contentType string) bool {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)), "audio/") {
		return true
	}
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".wav", ".mp3", ".m4a", ".aac", ".flac", ".ogg", ".opus":
		return true
	default:
		return false
	}
}
