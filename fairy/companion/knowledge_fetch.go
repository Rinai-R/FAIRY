package companion

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"fairy/memory"

	"golang.org/x/net/html"
)

const (
	knowledgeFetchTimeout      = 12 * time.Second
	knowledgeFetchMaxBodyBytes = 1 << 20
	knowledgeFetchMaxRedirects = 5
	knowledgeChunkRunes        = 1200
	knowledgeChunkOverlapRunes = 120
	knowledgeMaxChunksPerPage  = 32
	knowledgeMaxChunksPerBatch = 128
)

var (
	errKnowledgeFetchRejected  = errors.New("knowledge document fetch rejected")
	errKnowledgeFetchTransient = errors.New("knowledge document fetch transient failure")
)

type knowledgeResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type knowledgeDocumentFetcher interface {
	FetchSource(context.Context, memory.KnowledgeIngestSource) (memory.KnowledgeDocument, error)
}

type httpKnowledgeDocumentFetcher struct {
	resolver knowledgeResolver
	client   *http.Client
	dialer   *net.Dialer
}

func newHTTPKnowledgeDocumentFetcher() *httpKnowledgeDocumentFetcher {
	fetcher := &httpKnowledgeDocumentFetcher{
		resolver: net.DefaultResolver,
		dialer:   &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second},
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 8 * time.Second,
		DialContext:           fetcher.dialContext,
	}
	fetcher.client = &http.Client{
		Transport:     transport,
		Timeout:       knowledgeFetchTimeout,
		CheckRedirect: fetcher.checkRedirect,
	}
	return fetcher
}

func (f *httpKnowledgeDocumentFetcher) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= knowledgeFetchMaxRedirects {
		return fmt.Errorf("%w: redirect limit exceeded", errKnowledgeFetchRejected)
	}
	return f.validateURL(req.Context(), req.URL)
}

func (f *httpKnowledgeDocumentFetcher) FetchSource(ctx context.Context, source memory.KnowledgeIngestSource) (memory.KnowledgeDocument, error) {
	if f == nil || f.client == nil || f.resolver == nil || f.dialer == nil {
		return memory.KnowledgeDocument{}, errors.New("knowledge document fetcher is unavailable")
	}
	document, err := f.fetch(ctx, source)
	if err != nil {
		return memory.KnowledgeDocument{}, err
	}
	if len(document.Chunks) > knowledgeMaxChunksPerBatch {
		return memory.KnowledgeDocument{}, fmt.Errorf("%w: document chunk limit exceeded", errKnowledgeFetchRejected)
	}
	return document, nil
}

func (f *httpKnowledgeDocumentFetcher) fetch(ctx context.Context, source memory.KnowledgeIngestSource) (memory.KnowledgeDocument, error) {
	parsed, err := url.Parse(source.URL)
	if err != nil {
		return memory.KnowledgeDocument{}, fmt.Errorf("%w: invalid URL", errKnowledgeFetchRejected)
	}
	if err := f.validateURL(ctx, parsed); err != nil {
		return memory.KnowledgeDocument{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return memory.KnowledgeDocument{}, fmt.Errorf("%w: %v", errKnowledgeFetchRejected, err)
	}
	request.Header.Set("Accept", "text/html, text/plain;q=0.8")
	request.Header.Set("User-Agent", "FAIRY-KnowledgeFetcher/1")
	response, err := f.client.Do(request)
	if err != nil {
		if errors.Is(err, errKnowledgeFetchRejected) {
			return memory.KnowledgeDocument{}, err
		}
		return memory.KnowledgeDocument{}, fmt.Errorf("%w: %v", errKnowledgeFetchTransient, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500 {
		return memory.KnowledgeDocument{}, fmt.Errorf("%w: upstream status %d", errKnowledgeFetchTransient, response.StatusCode)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return memory.KnowledgeDocument{}, fmt.Errorf("%w: upstream status %d", errKnowledgeFetchRejected, response.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "text/html" && mediaType != "text/plain" {
		return memory.KnowledgeDocument{}, fmt.Errorf("%w: unsupported content type", errKnowledgeFetchRejected)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, knowledgeFetchMaxBodyBytes+1))
	if err != nil {
		return memory.KnowledgeDocument{}, fmt.Errorf("%w: %v", errKnowledgeFetchTransient, err)
	}
	if len(body) > knowledgeFetchMaxBodyBytes {
		return memory.KnowledgeDocument{}, fmt.Errorf("%w: body limit exceeded", errKnowledgeFetchRejected)
	}
	text, err := cleanKnowledgeDocument(mediaType, body)
	if err != nil {
		return memory.KnowledgeDocument{}, err
	}
	contentSum := sha256.Sum256([]byte(text))
	contentHash := fmt.Sprintf("%x", contentSum[:])
	chunks := chunkKnowledgeDocument(parsed.String(), contentHash, text)
	if len(chunks) == 0 {
		return memory.KnowledgeDocument{}, fmt.Errorf("%w: document has no usable text", errKnowledgeFetchRejected)
	}
	return memory.KnowledgeDocument{
		SourceID: source.ID, CanonicalURL: parsed.String(), Title: source.Title,
		ContentHash: contentHash, ContentType: mediaType,
		ETag:            strings.TrimSpace(response.Header.Get("ETag")),
		LastModified:    strings.TrimSpace(response.Header.Get("Last-Modified")),
		FetchedAtUnixMS: time.Now().UnixMilli(), Chunks: chunks,
	}, nil
}

func (f *httpKnowledgeDocumentFetcher) validateURL(ctx context.Context, parsed *url.URL) error {
	if parsed == nil || parsed.User != nil || parsed.Hostname() == "" {
		return fmt.Errorf("%w: URL authority is invalid", errKnowledgeFetchRejected)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("%w: URL scheme is invalid", errKnowledgeFetchRejected)
	}
	addresses, err := f.resolver.LookupNetIP(ctx, "ip", parsed.Hostname())
	if err != nil {
		return fmt.Errorf("%w: DNS lookup failed: %v", errKnowledgeFetchTransient, err)
	}
	if len(addresses) == 0 {
		return fmt.Errorf("%w: DNS returned no addresses", errKnowledgeFetchTransient)
	}
	for _, address := range addresses {
		if !isPublicKnowledgeAddress(address) {
			return fmt.Errorf("%w: URL resolves to a non-public address", errKnowledgeFetchRejected)
		}
	}
	return nil
}

func (f *httpKnowledgeDocumentFetcher) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("%w: dial address is invalid", errKnowledgeFetchRejected)
	}
	addresses, err := f.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("%w: DNS lookup failed: %v", errKnowledgeFetchTransient, err)
	}
	for _, candidate := range addresses {
		if !isPublicKnowledgeAddress(candidate) {
			return nil, fmt.Errorf("%w: dial target is non-public", errKnowledgeFetchRejected)
		}
		connection, dialErr := f.dialer.DialContext(ctx, network, net.JoinHostPort(candidate.String(), port))
		if dialErr == nil {
			return connection, nil
		}
		err = dialErr
	}
	return nil, fmt.Errorf("%w: dial failed: %v", errKnowledgeFetchTransient, err)
}

func isPublicKnowledgeAddress(address netip.Addr) bool {
	address = address.Unmap()
	return address.IsValid() &&
		!address.IsPrivate() &&
		!address.IsLoopback() &&
		!address.IsLinkLocalUnicast() &&
		!address.IsLinkLocalMulticast() &&
		!address.IsMulticast() &&
		!address.IsUnspecified()
}

func cleanKnowledgeDocument(mediaType string, body []byte) (string, error) {
	if mediaType == "text/plain" {
		text := strings.Join(strings.Fields(string(body)), " ")
		if !utf8.ValidString(text) || text == "" {
			return "", fmt.Errorf("%w: text body is invalid", errKnowledgeFetchRejected)
		}
		return text, nil
	}
	tokenizer := html.NewTokenizer(strings.NewReader(string(body)))
	var text strings.Builder
	skipDepth := 0
	for {
		switch tokenType := tokenizer.Next(); tokenType {
		case html.ErrorToken:
			if errors.Is(tokenizer.Err(), io.EOF) {
				cleaned := strings.Join(strings.Fields(text.String()), " ")
				if cleaned == "" {
					return "", fmt.Errorf("%w: HTML has no usable text", errKnowledgeFetchRejected)
				}
				return cleaned, nil
			}
			return "", fmt.Errorf("%w: invalid HTML", errKnowledgeFetchRejected)
		case html.StartTagToken:
			name, _ := tokenizer.TagName()
			if isSkippedKnowledgeElement(string(name)) {
				skipDepth++
			}
		case html.EndTagToken:
			name, _ := tokenizer.TagName()
			if isSkippedKnowledgeElement(string(name)) && skipDepth > 0 {
				skipDepth--
			}
		case html.TextToken:
			if skipDepth == 0 {
				text.Write(tokenizer.Text())
				text.WriteByte(' ')
			}
		}
	}
}

func isSkippedKnowledgeElement(name string) bool {
	switch strings.ToLower(name) {
	case "script", "style", "noscript", "svg", "nav", "footer", "form":
		return true
	default:
		return false
	}
}

func chunkKnowledgeDocument(canonicalURL, contentHash, text string) []memory.KnowledgeDocumentChunk {
	runes := []rune(text)
	if len(runes) == 0 {
		return nil
	}
	chunks := make([]memory.KnowledgeDocumentChunk, 0, min((len(runes)+knowledgeChunkRunes-1)/knowledgeChunkRunes, knowledgeMaxChunksPerPage))
	for start, ordinal := 0, 0; start < len(runes) && ordinal < knowledgeMaxChunksPerPage; ordinal++ {
		end := min(start+knowledgeChunkRunes, len(runes))
		chunkText := strings.TrimSpace(string(runes[start:end]))
		if chunkText != "" {
			textSum := sha256.Sum256([]byte(chunkText))
			idSum := sha256.Sum256([]byte(canonicalURL + "\x00" + contentHash + "\x00" + fmt.Sprintf("%x", textSum[:]) + "\x00" + strconv.Itoa(ordinal)))
			chunks = append(chunks, memory.KnowledgeDocumentChunk{
				ID: fmt.Sprintf("web-chunk-%x", idSum[:12]), Ordinal: ordinal,
				Text: chunkText, TextHash: fmt.Sprintf("%x", textSum[:]),
			})
		}
		if end == len(runes) {
			break
		}
		start = end - knowledgeChunkOverlapRunes
	}
	return chunks
}
