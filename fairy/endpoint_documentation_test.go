package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestEndpointDocumentationPinsExternalProviderBoundary(t *testing.T) {
	files := []string{
		filepath.Join("..", "README.md"),
		filepath.Join("..", "docs", "getting-started.md"),
		filepath.Join("..", "docs", "troubleshooting.md"),
		filepath.Join("..", "docs", "plugins.md"),
		filepath.Join("..", "docs", "release-validation.md"),
		filepath.Join("..", "docs", "qq-onebot.md"),
	}
	var combined strings.Builder
	for _, filename := range files {
		raw, err := os.ReadFile(filename)
		if err != nil {
			t.Fatalf("read endpoint documentation %s: %v", filename, err)
		}
		combined.Write(raw)
		combined.WriteByte('\n')
	}
	text := combined.String()

	for _, required := range []string{
		"第三方聊天",
		"第三方 semantic embedding",
		"1024 维",
		"OpenSERP",
		"SecretStore",
		"endpoint-strict",
		"非严格",
		"本地历史",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("endpoint documentation does not state required boundary %q", required)
		}
	}

	for _, obsolete := range []string{
		"QQ 与搜索是显式安装的本地插件",
		"需要 QQ 或搜索时再安装对应插件",
		"检查插件实例是否 ready、是否授予 `http.request`",
		"搜索工具失败且可诊断",
	} {
		if strings.Contains(text, obsolete) {
			t.Errorf("endpoint documentation retains obsolete runtime narrative %q", obsolete)
		}
	}

	secretPattern := regexp.MustCompile(`(?i)(?:sk|sf|pk)-live-[a-z0-9_-]+|bearer\s+[a-z0-9._~+/=-]{8,}|(?:api[_ -]?key|token|password)\s*[:=]\s*["']?[a-z0-9._~+/=-]{8,}`)
	if match := secretPattern.FindString(text); match != "" {
		t.Fatalf("endpoint documentation contains credential-like material %q", match)
	}

	positiveLocalModel := regexp.MustCompile(`(?i)(?:安装|下载|启动|运行|部署|配置|依赖).{0,24}(?:ollama|llama\.cpp|本地\s*(?:llm|模型|embedding|嵌入))|(?:ollama|llama\.cpp|本地\s*(?:llm|模型|embedding|嵌入)).{0,24}(?:必需|需要|依赖|推荐)`)
	for _, line := range strings.Split(text, "\n") {
		if !positiveLocalModel.MatchString(line) || endpointDocumentationDeniesRequirement(line) {
			continue
		}
		t.Errorf("endpoint documentation recommends a forbidden local-model dependency: %s", strings.TrimSpace(line))
	}

	for _, forbidden := range []string{"export FAIRY_", "FAIRY_MODEL_ENDPOINT=", "FAIRY_EMBEDDING_ENDPOINT=", "HTTP_PROXY=", "HTTPS_PROXY="} {
		if strings.Contains(text, forbidden) {
			t.Errorf("endpoint documentation configures strict runtime through environment: %s", forbidden)
		}
	}
}

func endpointDocumentationDeniesRequirement(line string) bool {
	for _, denial := range []string{
		"不启动",
		"不携带",
		"不下载",
		"不要求",
		"不依赖",
		"不属于",
		"不读取",
		"不安装",
		"不要",
		"无需",
		"不得",
		"禁止",
		"不会",
		"未安装",
		"不可用",
	} {
		if strings.Contains(line, denial) {
			return true
		}
	}
	return false
}
