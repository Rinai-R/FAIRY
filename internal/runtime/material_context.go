package runtime

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Rinai-R/FAIRY/internal/app"
)

const (
	maxMaterialContextTextBytes = 256 << 10
	maxMaterialBriefBytes       = 96 << 10
	maxMaterialSingleFileBytes  = 256 << 10
)

var (
	htmlTagPattern        = regexp.MustCompile(`(?s)<[^>]+>`)
	htmlWhitespacePattern = regexp.MustCompile(`[ \t\r\n]+`)
)

type materialTextPart struct {
	title string
	item  app.MaterialItem
	text  string
}

func (r *Runtime) prepareMaterialContext(ctx context.Context, req app.SceneGenerateRequest) (app.SceneGenerateRequest, error) {
	source, err := normalizeMaterialSource(req)
	if err != nil {
		return app.SceneGenerateRequest{}, err
	}
	if source.Mode == "" {
		return req, nil
	}
	req.MaterialSource = source
	if strings.TrimSpace(req.MaterialContext.Brief) != "" {
		return req, nil
	}
	material, err := r.buildMaterialContext(ctx, source)
	if err != nil {
		return app.SceneGenerateRequest{}, err
	}
	req.MaterialContext = material
	return req, nil
}

func normalizeMaterialSource(req app.SceneGenerateRequest) (app.MaterialSource, error) {
	if req.MaterialSource.Mode != "" {
		return validateMaterialSource(req.MaterialSource)
	}
	return app.MaterialSource{}, nil
}

func validateMaterialSource(source app.MaterialSource) (app.MaterialSource, error) {
	switch source.Mode {
	case app.MaterialSourceText:
		source.Text = strings.TrimSpace(source.Text)
		if source.Text == "" {
			return app.MaterialSource{}, errors.New("material_source.text 不能为空")
		}
	case app.MaterialSourceUploadedFile:
		source.AssetPath = strings.TrimSpace(source.AssetPath)
		if source.AssetPath == "" {
			return app.MaterialSource{}, errors.New("material_source.asset_path 不能为空")
		}
	default:
		return app.MaterialSource{}, fmt.Errorf("material_source.mode 不支持: %q", source.Mode)
	}
	return source, nil
}

func (r *Runtime) buildMaterialContext(_ context.Context, source app.MaterialSource) (app.MaterialContext, error) {
	switch source.Mode {
	case app.MaterialSourceText:
		item := app.MaterialItem{
			SourceType: "text",
			Status:     "read",
			TextBytes:  len(source.Text),
			SizeBytes:  int64(len(source.Text)),
		}
		return buildMaterialContextFromParts(source, []materialTextPart{{
			title: "粘贴正文",
			item:  item,
			text:  source.Text,
		}}, nil)
	case app.MaterialSourceUploadedFile:
		return r.materialContextFromUploadedFile(source)
	default:
		return app.MaterialContext{}, fmt.Errorf("material_source.mode 不支持: %q", source.Mode)
	}
}

func (r *Runtime) materialContextFromUploadedFile(source app.MaterialSource) (app.MaterialContext, error) {
	path, err := r.materialAssetPath(source.AssetPath)
	if err != nil {
		return app.MaterialContext{}, fmt.Errorf("material.uploaded_file: %w", err)
	}
	text, item, err := readMaterialFile(path, source.AssetType, "uploaded_file")
	if err != nil {
		return app.MaterialContext{}, err
	}
	if source.AssetName != "" {
		item.Filename = source.AssetName
	}
	return buildMaterialContextFromParts(source, []materialTextPart{{
		title: firstNonEmpty(source.AssetName, filepath.Base(path)),
		item:  item,
		text:  text,
	}}, nil)
}

func buildMaterialContextFromParts(source app.MaterialSource, parts []materialTextPart, skipped []app.MaterialItem) (app.MaterialContext, error) {
	if len(parts) == 0 {
		return app.MaterialContext{}, fmt.Errorf("material.%s: 未读取到可用文本材料", source.Mode)
	}
	var textBuilder strings.Builder
	items := make([]app.MaterialItem, 0, len(parts)+len(skipped))
	var total int64
	truncated := false
	for _, part := range parts {
		if strings.TrimSpace(part.text) == "" {
			continue
		}
		before := textBuilder.Len()
		textBuilder.WriteString("\n\n## ")
		textBuilder.WriteString(strings.TrimSpace(part.title))
		textBuilder.WriteString("\n")
		textBuilder.WriteString(strings.TrimSpace(part.text))
		if textBuilder.Len() > maxMaterialContextTextBytes {
			truncated = true
			break
		}
		item := part.item
		item.Status = firstNonEmpty(item.Status, "read")
		item.TextBytes = len(part.text)
		total += int64(item.TextBytes)
		items = append(items, item)
		if textBuilder.Len() == before {
			break
		}
	}
	items = append(items, skipped...)
	fullText := truncateMaterialText(textBuilder.String())
	brief := truncateString(fullText, maxMaterialBriefBytes)
	if strings.TrimSpace(brief) == "" {
		return app.MaterialContext{}, fmt.Errorf("material.%s: 材料文本为空", source.Mode)
	}
	report := app.MaterialSourceReport{
		Mode:       source.Mode,
		Summary:    fmt.Sprintf("读取 %d 个材料源，跳过 %d 个，约 %d bytes 文本", len(parts), len(skipped), total),
		Items:      items,
		TotalBytes: total,
		Truncated:  truncated || len(fullText) > len(brief),
	}
	return app.MaterialContext{
		Brief:  brief,
		Text:   fullText,
		Report: report,
	}, nil
}

func readMaterialFile(path string, contentType string, sourceType string) (string, app.MaterialItem, error) {
	if !isAllowedMaterialPath(path, contentType) {
		return "", app.MaterialItem{}, fmt.Errorf("material.%s: 文件类型不在材料白名单: %s", sourceType, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", app.MaterialItem{}, fmt.Errorf("material.%s: 读取文件失败: %w", sourceType, err)
	}
	item := app.MaterialItem{
		SourceType: sourceType,
		Path:       path,
		Filename:   filepath.Base(path),
		SizeBytes:  info.Size(),
		Status:     "read",
	}
	if isPDFPath(path, contentType) {
		text, err := extractPDFText(path)
		if err != nil {
			return "", app.MaterialItem{}, fmt.Errorf("material.%s: %w", sourceType, err)
		}
		item.ContentType = firstNonEmpty(contentType, "application/pdf")
		item.TextBytes = len(text)
		return text, item, nil
	}
	if isDOCXPath(path, contentType) {
		text, err := extractDOCXText(path)
		if err != nil {
			return "", app.MaterialItem{}, fmt.Errorf("material.%s: %w", sourceType, err)
		}
		item.ContentType = firstNonEmpty(contentType, "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
		item.TextBytes = len(text)
		return text, item, nil
	}
	data, fileTruncated, err := readLimitedFile(path, maxMaterialSingleFileBytes)
	if err != nil {
		return "", app.MaterialItem{}, fmt.Errorf("material.%s: 读取文本文件失败: %w", sourceType, err)
	}
	text := string(data)
	if isHTMLPath(path, contentType) {
		text = stripHTMLText(text)
		item.ContentType = firstNonEmpty(contentType, "text/html")
	} else {
		item.ContentType = firstNonEmpty(contentType, "text/plain")
	}
	item.TextBytes = len(text)
	item.Truncated = fileTruncated
	if strings.TrimSpace(text) == "" {
		return "", app.MaterialItem{}, fmt.Errorf("material.%s: 文件没有可用文本: %s", sourceType, path)
	}
	return text, item, nil
}

func extractMaterialBytes(filename string, contentType string, data []byte, sourceType string) (string, app.MaterialItem, error) {
	item := app.MaterialItem{
		SourceType:  sourceType,
		Filename:    filename,
		ContentType: contentType,
		SizeBytes:   int64(len(data)),
		Status:      "read",
	}
	if !isAllowedMaterialPath(filename, contentType) {
		return "", app.MaterialItem{}, fmt.Errorf("material.%s: 文件类型不在材料白名单: %s", sourceType, filename)
	}
	if isPDFPath(filename, contentType) {
		tmp, err := os.CreateTemp("", "fairy-material-*.pdf")
		if err != nil {
			return "", app.MaterialItem{}, fmt.Errorf("material.%s: 创建临时 PDF 失败: %w", sourceType, err)
		}
		tmpPath := tmp.Name()
		defer os.Remove(tmpPath)
		if _, err := tmp.Write(data); err != nil {
			tmp.Close()
			return "", app.MaterialItem{}, fmt.Errorf("material.%s: 写入临时 PDF 失败: %w", sourceType, err)
		}
		if err := tmp.Close(); err != nil {
			return "", app.MaterialItem{}, fmt.Errorf("material.%s: 关闭临时 PDF 失败: %w", sourceType, err)
		}
		text, err := extractPDFText(tmpPath)
		if err != nil {
			return "", app.MaterialItem{}, fmt.Errorf("material.%s: %w", sourceType, err)
		}
		item.TextBytes = len(text)
		return text, item, nil
	}
	if isDOCXPath(filename, contentType) {
		text, err := extractDOCXBytes(data)
		if err != nil {
			return "", app.MaterialItem{}, fmt.Errorf("material.%s: %w", sourceType, err)
		}
		item.TextBytes = len(text)
		return text, item, nil
	}
	if len(data) > maxMaterialSingleFileBytes {
		data = data[:maxMaterialSingleFileBytes]
		item.Truncated = true
	}
	text := string(data)
	if isHTMLPath(filename, contentType) {
		text = stripHTMLText(text)
	}
	item.TextBytes = len(text)
	if strings.TrimSpace(text) == "" {
		return "", app.MaterialItem{}, fmt.Errorf("material.%s: 文档没有可用文本: %s", sourceType, filename)
	}
	return text, item, nil
}

func isAllowedMaterialPath(path string, contentType string) bool {
	lowerType := strings.ToLower(strings.TrimSpace(contentType))
	if strings.HasPrefix(lowerType, "text/") ||
		strings.Contains(lowerType, "json") ||
		strings.Contains(lowerType, "xml") ||
		lowerType == "application/pdf" ||
		lowerType == "application/vnd.openxmlformats-officedocument.wordprocessingml.document" {
		return true
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".txt", ".md", ".markdown", ".csv", ".json", ".yaml", ".yml",
		".html", ".htm", ".xml", ".pdf", ".docx",
		".go", ".js", ".jsx", ".ts", ".tsx", ".py", ".java", ".rs", ".c", ".cc", ".cpp", ".h", ".hpp",
		".css", ".scss", ".sql", ".sh", ".toml", ".ini", ".conf":
		return true
	default:
		return false
	}
}

func isPDFPath(path string, contentType string) bool {
	return strings.EqualFold(filepath.Ext(path), ".pdf") || strings.EqualFold(strings.TrimSpace(contentType), "application/pdf")
}

func isDOCXPath(path string, contentType string) bool {
	return strings.EqualFold(filepath.Ext(path), ".docx") ||
		strings.EqualFold(strings.TrimSpace(contentType), "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
}

func isHTMLPath(path string, contentType string) bool {
	lowerType := strings.ToLower(strings.TrimSpace(contentType))
	ext := strings.ToLower(filepath.Ext(path))
	return strings.Contains(lowerType, "html") || ext == ".html" || ext == ".htm"
}

func extractDOCXText(path string) (string, error) {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return "", fmt.Errorf("读取 docx 失败: %w", err)
	}
	defer reader.Close()
	return extractDOCXTextFromFiles(reader.File)
}

func extractDOCXBytes(data []byte) (string, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("读取 docx 失败: %w", err)
	}
	return extractDOCXTextFromFiles(reader.File)
}

func extractDOCXTextFromFiles(files []*zip.File) (string, error) {
	for _, file := range files {
		if file.Name != "word/document.xml" {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return "", fmt.Errorf("打开 word/document.xml 失败: %w", err)
		}
		defer rc.Close()
		text, err := extractWordText(rc)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(text) == "" {
			return "", errors.New("docx 没有可用文本")
		}
		return text, nil
	}
	return "", errors.New("docx 缺少 word/document.xml")
}

func extractWordText(r io.Reader) (string, error) {
	decoder := xml.NewDecoder(r)
	var builder strings.Builder
	inText := false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("解析 docx 文本失败: %w", err)
		}
		switch value := token.(type) {
		case xml.CharData:
			if inText {
				builder.Write([]byte(value))
			}
		case xml.StartElement:
			switch value.Name.Local {
			case "t":
				inText = true
			case "br", "cr":
				appendLineBreak(&builder)
			case "tab":
				builder.WriteByte('\t')
			}
		case xml.EndElement:
			switch value.Name.Local {
			case "t":
				inText = false
			case "p":
				appendLineBreak(&builder)
			}
		}
	}
	return normalizeExtractedDOCXText(builder.String()), nil
}

func appendLineBreak(builder *strings.Builder) {
	if builder.Len() == 0 {
		return
	}
	text := builder.String()
	if !strings.HasSuffix(text, "\n") {
		builder.WriteByte('\n')
	}
}

func normalizeExtractedDOCXText(value string) string {
	var lines []string
	for _, line := range strings.Split(value, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func readLimitedFile(path string, limit int64) ([]byte, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	limited := io.LimitReader(file, limit+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > limit {
		return data[:limit], true, nil
	}
	return data, false, nil
}

func stripHTMLText(value string) string {
	text := htmlTagPattern.ReplaceAllString(value, " ")
	text = html.UnescapeString(text)
	text = htmlWhitespacePattern.ReplaceAllString(text, " ")
	return strings.TrimSpace(text)
}

func truncateString(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	return value[:maxBytes]
}
