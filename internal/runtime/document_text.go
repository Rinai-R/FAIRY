package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	pdf "github.com/ledongthuc/pdf"

	"github.com/Rinai-R/FAIRY/internal/app"
)

const maxInjectedDocumentTextBytes = 256 << 10

func (r *Runtime) hydrateSceneDocumentText(req app.SceneGenerateRequest) (app.SceneGenerateRequest, error) {
	if strings.TrimSpace(req.DocumentText) != "" {
		return req, nil
	}
	path := documentAssetPath(req.Variables)
	if path == "" {
		return req, nil
	}
	materialPath, err := r.materialAssetPath(path)
	if err != nil {
		return app.SceneGenerateRequest{}, err
	}
	text, source, err := extractDocumentText(req.Variables, materialPath)
	if err != nil {
		return app.SceneGenerateRequest{}, err
	}
	text = truncateDocumentText(text)
	if strings.TrimSpace(text) == "" {
		return req, nil
	}
	req.DocumentText = text
	req.Variables = cloneVariables(req.Variables)
	req.Variables["document_text_source"] = source
	return req, nil
}

func extractDocumentText(variables map[string]string, path string) (string, string, error) {
	if isTextDocumentAsset(variables, path) {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", "", fmt.Errorf("读取上传文本材料失败: %w", err)
		}
		return string(data), "uploaded_text_asset", nil
	}
	if isPDFDocumentAsset(variables, path) {
		text, err := extractPDFText(path)
		if err != nil {
			return "", "", err
		}
		return text, "uploaded_pdf_text_layer", nil
	}
	return "", "", nil
}

func extractPDFText(path string) (string, error) {
	file, reader, err := pdf.Open(path)
	if err != nil {
		return "", fmt.Errorf("打开 PDF 材料失败: %w", err)
	}
	defer file.Close()

	var out strings.Builder
	for pageIndex := 1; pageIndex <= reader.NumPage(); pageIndex++ {
		page := reader.Page(pageIndex)
		if page.V.IsNull() {
			continue
		}
		text, err := page.GetPlainText(nil)
		if err != nil {
			return "", fmt.Errorf("提取 PDF 第 %d 页文本失败: %w", pageIndex, err)
		}
		out.WriteString(text)
		out.WriteString("\n\n")
		if out.Len() >= maxInjectedDocumentTextBytes {
			break
		}
	}
	return out.String(), nil
}

func truncateDocumentText(text string) string {
	if len(text) <= maxInjectedDocumentTextBytes {
		return text
	}
	return text[:maxInjectedDocumentTextBytes]
}

func (r *Runtime) materialAssetPath(path string) (string, error) {
	materialDir := r.materialDir
	if materialDir == "" {
		materialDir = DefaultMaterialDir
	}
	root, err := filepath.Abs(materialDir)
	if err != nil {
		return "", fmt.Errorf("解析材料目录失败: %w", err)
	}
	target, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("解析材料文件路径失败: %w", err)
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return "", fmt.Errorf("校验材料文件路径失败: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("材料文件不在材料目录内: %s", path)
	}
	return target, nil
}

func documentAssetPath(variables map[string]string) string {
	for _, key := range []string{"document_asset_path", "material_file_path"} {
		if value := strings.TrimSpace(variables[key]); value != "" {
			return value
		}
	}
	return ""
}

func isTextDocumentAsset(variables map[string]string, path string) bool {
	contentType := strings.ToLower(strings.TrimSpace(variables["document_asset_type"]))
	if strings.HasPrefix(contentType, "text/") {
		return true
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".txt", ".md", ".markdown", ".csv", ".json", ".yaml", ".yml":
		return true
	default:
		return false
	}
}

func isPDFDocumentAsset(variables map[string]string, path string) bool {
	contentType := strings.ToLower(strings.TrimSpace(variables["document_asset_type"]))
	return contentType == "application/pdf" || strings.EqualFold(filepath.Ext(path), ".pdf")
}

func cloneVariables(values map[string]string) map[string]string {
	out := map[string]string{}
	for key, value := range values {
		out[key] = value
	}
	return out
}
