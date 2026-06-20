package runtime

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/Rinai-R/FAIRY/internal/app"
)

const (
	maxMaterialContextTextBytes = 256 << 10
	maxMaterialBriefBytes       = 96 << 10
	maxMaterialDirectoryFiles   = 32
	maxMaterialSingleFileBytes  = 256 << 10
	maxMaterialTotalBytes       = 512 << 10
)

var (
	localPathCandidatePattern = regexp.MustCompile(`(?:~[/\\]|/)[^，。,;\s]+`)
	htmlTagPattern            = regexp.MustCompile(`(?s)<[^>]+>`)
	htmlWhitespacePattern     = regexp.MustCompile(`[ \t\r\n]+`)
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
		if strings.TrimSpace(req.DocumentText) == "" {
			req.DocumentText = firstNonEmpty(req.MaterialContext.Text, req.MaterialContext.Brief)
		}
		return req, nil
	}
	material, err := r.buildMaterialContext(ctx, source)
	if err != nil {
		return app.SceneGenerateRequest{}, err
	}
	req.MaterialContext = material
	req.DocumentText = material.Text
	req.Variables = cloneVariables(req.Variables)
	req.Variables["material_source_mode"] = string(source.Mode)
	req.Variables["material_source_summary"] = material.Report.Summary
	return req, nil
}

func normalizeMaterialSource(req app.SceneGenerateRequest) (app.MaterialSource, error) {
	if req.MaterialSource.Mode != "" {
		return validateMaterialSource(req.MaterialSource)
	}
	mode := strings.TrimSpace(req.Variables["source_mode"])
	switch mode {
	case "directory", "local_directory":
		return validateMaterialSource(app.MaterialSource{
			Mode: app.MaterialSourceLocalDirectory,
			Path: firstNonEmpty(
				req.Variables["local_directory_path"],
				extractLocalPath(req.DocumentText),
				extractLocalPath(req.Variables["local_directory_instruction"]),
			),
			DisplayName: "本地目录",
		})
	case "url":
		return validateMaterialSource(app.MaterialSource{
			Mode: app.MaterialSourceURL,
			URL:  firstNonEmpty(req.Variables["document_url"], req.Variables["source_url"]),
		})
	case "uploaded_file":
		return validateMaterialSource(app.MaterialSource{
			Mode:      app.MaterialSourceUploadedFile,
			AssetID:   req.Variables["document_asset_id"],
			AssetName: req.Variables["document_asset_name"],
			AssetType: req.Variables["document_asset_type"],
			AssetPath: firstNonEmpty(req.Variables["document_asset_path"], req.Variables["material_file_path"]),
		})
	case "text":
		return validateMaterialSource(app.MaterialSource{Mode: app.MaterialSourceText, Text: req.DocumentText})
	}
	if strings.TrimSpace(req.DocumentText) != "" {
		return validateMaterialSource(app.MaterialSource{Mode: app.MaterialSourceText, Text: req.DocumentText})
	}
	if url := firstNonEmpty(req.Variables["document_url"], req.Variables["source_url"]); url != "" {
		return validateMaterialSource(app.MaterialSource{Mode: app.MaterialSourceURL, URL: url})
	}
	if path := firstNonEmpty(req.Variables["document_asset_path"], req.Variables["material_file_path"]); path != "" {
		return validateMaterialSource(app.MaterialSource{
			Mode:      app.MaterialSourceUploadedFile,
			AssetID:   req.Variables["document_asset_id"],
			AssetName: req.Variables["document_asset_name"],
			AssetType: req.Variables["document_asset_type"],
			AssetPath: path,
		})
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
		source.AssetPath = strings.TrimSpace(firstNonEmpty(source.AssetPath, source.Path))
		if source.AssetPath == "" {
			return app.MaterialSource{}, errors.New("material_source.asset_path 不能为空")
		}
	case app.MaterialSourceURL:
		source.URL = strings.TrimSpace(source.URL)
		if source.URL == "" {
			return app.MaterialSource{}, errors.New("material_source.url 不能为空")
		}
	case app.MaterialSourceLocalDirectory:
		source.Path = strings.TrimSpace(source.Path)
		if source.Path == "" {
			return app.MaterialSource{}, errors.New("material_source.path 不能为空")
		}
	default:
		return app.MaterialSource{}, fmt.Errorf("material_source.mode 不支持: %q", source.Mode)
	}
	return source, nil
}

func (r *Runtime) buildMaterialContext(ctx context.Context, source app.MaterialSource) (app.MaterialContext, error) {
	switch source.Mode {
	case app.MaterialSourceText:
		item := app.MaterialItem{
			SourceType: "text",
			Status:     "read",
			TextBytes:  len(source.Text),
			SizeBytes:  int64(len(source.Text)),
		}
		return buildMaterialContextFromParts(source, []materialTextPart{{
			title: firstNonEmpty(source.DisplayName, "粘贴正文"),
			item:  item,
			text:  source.Text,
		}}, nil)
	case app.MaterialSourceUploadedFile:
		return r.materialContextFromUploadedFile(source)
	case app.MaterialSourceURL:
		return r.materialContextFromURL(ctx, source)
	case app.MaterialSourceLocalDirectory:
		return r.materialContextFromLocalPath(source)
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

func (r *Runtime) materialContextFromURL(ctx context.Context, source app.MaterialSource) (app.MaterialContext, error) {
	resp, err := r.FetchDocument(ctx, app.DocumentFetchRequest{URL: source.URL})
	if err != nil {
		return app.MaterialContext{}, fmt.Errorf("material.url: %w", err)
	}
	data, err := base64.StdEncoding.DecodeString(resp.DataBase64)
	if err != nil {
		return app.MaterialContext{}, fmt.Errorf("material.url: document data 不是有效 base64: %w", err)
	}
	text, item, err := extractMaterialBytes(resp.Filename, resp.ContentType, data, "url")
	if err != nil {
		return app.MaterialContext{}, err
	}
	item.URL = resp.URL
	item.Filename = resp.Filename
	item.ContentType = resp.ContentType
	item.SizeBytes = resp.SizeBytes
	return buildMaterialContextFromParts(source, []materialTextPart{{
		title: firstNonEmpty(resp.Filename, resp.URL),
		item:  item,
		text:  text,
	}}, nil)
}

func (r *Runtime) materialContextFromLocalPath(source app.MaterialSource) (app.MaterialContext, error) {
	root, err := resolveLocalMaterialPath(source.Path)
	if err != nil {
		return app.MaterialContext{}, fmt.Errorf("material.local_directory: 解析路径失败: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return app.MaterialContext{}, fmt.Errorf("material.local_directory: 读取路径失败: %w", err)
	}
	if !info.IsDir() {
		text, item, err := readMaterialFile(root, "", "local_file")
		if err != nil {
			return app.MaterialContext{}, err
		}
		return buildMaterialContextFromParts(source, []materialTextPart{{
			title: filepath.Base(root),
			item:  item,
			text:  text,
		}}, nil)
	}

	var parts []materialTextPart
	var skipped []app.MaterialItem
	var total int64
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			skipped = append(skipped, app.MaterialItem{SourceType: "local_file", Path: path, Status: "skipped", Message: walkErr.Error()})
			return nil
		}
		if entry.IsDir() {
			if path != root && shouldSkipMaterialDirectory(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if len(parts) >= maxMaterialDirectoryFiles {
			skipped = append(skipped, app.MaterialItem{SourceType: "local_file", Path: path, Status: "skipped", Message: "超过目录读取文件数量上限"})
			return nil
		}
		if !isAllowedMaterialPath(path, "") {
			skipped = append(skipped, app.MaterialItem{SourceType: "local_file", Path: path, Status: "skipped", Message: "文件类型不在材料白名单"})
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			skipped = append(skipped, app.MaterialItem{SourceType: "local_file", Path: path, Status: "skipped", Message: err.Error()})
			return nil
		}
		if total >= maxMaterialTotalBytes {
			skipped = append(skipped, app.MaterialItem{SourceType: "local_file", Path: path, Status: "skipped", Message: "超过材料总字节上限", SizeBytes: info.Size()})
			return nil
		}
		text, item, err := readMaterialFile(path, "", "local_file")
		if err != nil {
			skipped = append(skipped, app.MaterialItem{SourceType: "local_file", Path: path, Status: "skipped", Message: err.Error(), SizeBytes: info.Size()})
			return nil
		}
		total += int64(item.TextBytes)
		parts = append(parts, materialTextPart{title: relativeMaterialTitle(root, path), item: item, text: text})
		return nil
	})
	if err != nil {
		return app.MaterialContext{}, fmt.Errorf("material.local_directory: 枚举目录失败: %w", err)
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].title < parts[j].title })
	return buildMaterialContextFromParts(source, parts, skipped)
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
	fullText := truncateDocumentText(textBuilder.String())
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
		text, err := extractWordDocumentText(rc)
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

func extractWordDocumentText(r io.Reader) (string, error) {
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

func extractLocalPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if path, err := resolveLocalMaterialPath(value); err == nil {
		if _, err := os.Stat(path); err == nil {
			return value
		}
	}
	match := localPathCandidatePattern.FindString(value)
	return strings.TrimRight(match, "，。.,;；")
}

func resolveLocalMaterialPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("展开 ~ 失败: %w", err)
		}
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, path[2:])
		}
	}
	return filepath.Abs(filepath.Clean(path))
}

func relativeMaterialTitle(root string, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." {
		return filepath.Base(path)
	}
	return rel
}

func shouldSkipMaterialDirectory(name string) bool {
	switch strings.ToLower(name) {
	case ".git", "node_modules", "vendor", "dist", "build", ".next", ".venv", "venv", "__pycache__":
		return true
	default:
		return false
	}
}
