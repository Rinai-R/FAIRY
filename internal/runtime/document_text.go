package runtime

import (
	"fmt"
	"path/filepath"
	"strings"

	pdf "github.com/ledongthuc/pdf"
)

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
		if out.Len() >= maxMaterialContextTextBytes {
			break
		}
	}
	return out.String(), nil
}

func truncateMaterialText(text string) string {
	if len(text) <= maxMaterialContextTextBytes {
		return text
	}
	return text[:maxMaterialContextTextBytes]
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

func cloneVariables(values map[string]string) map[string]string {
	out := map[string]string{}
	for key, value := range values {
		out[key] = value
	}
	return out
}
