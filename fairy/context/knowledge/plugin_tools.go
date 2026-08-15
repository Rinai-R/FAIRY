package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/url"
	"strings"
	"time"

	"fairy/plugin"
	"fairy/plugin/sdk"
	"fairy/plugin/websearch"
)

var ErrPluginCapabilityUnavailable = errors.New("web plugin capability is unavailable")

type EnvelopeInvoker func(ctx context.Context, raw []byte) ([]byte, error)

type PluginTools struct {
	SearchInvoke EnvelopeInvoker
	NewFetch     func(target string) (EnvelopeInvoker, error)
	Resolver     knowledgeResolver
	Ready        bool
	BaseURL      string
	InstanceID   string
}

func (p *PluginTools) Available() bool {
	return p != nil && p.Ready && p.SearchInvoke != nil && strings.TrimSpace(p.InstanceID) != "" && strings.TrimSpace(p.BaseURL) != ""
}

func (p *PluginTools) Close() error {
	return nil
}

func (p *PluginTools) Search(ctx context.Context, query string, limit int) ([]WebSearchHit, error) {
	if !p.Available() {
		return nil, ErrPluginCapabilityUnavailable
	}
	payload, err := json.Marshal(map[string]any{
		"tool": websearch.ToolName,
		"arguments": map[string]any{
			"query": query,
			"limit": limit,
		},
		"config": map[string]any{"baseURL": p.BaseURL},
	})
	if err != nil {
		return nil, err
	}
	envelope, err := p.invoke(ctx, p.SearchInvoke, payload)
	if err != nil {
		return nil, err
	}
	result, err := websearch.DecodeSearchResult(envelope.Payload)
	if err != nil {
		return nil, fmt.Errorf("web search plugin result: %w", err)
	}
	hits := make([]WebSearchHit, 0, len(result.Sources))
	for _, source := range result.Sources {
		hits = append(hits, WebSearchHit{Title: source.Title, URL: source.URL, Snippet: source.Snippet})
	}
	return hits, nil
}

func (p *PluginTools) FetchSource(ctx context.Context, source IngestSource) (Document, error) {
	if p == nil || !p.Ready || p.NewFetch == nil {
		return Document{}, ErrPluginCapabilityUnavailable
	}
	parsed, err := url.Parse(strings.TrimSpace(source.URL))
	if err != nil {
		return Document{}, fmt.Errorf("%w: invalid URL", ErrFetchRejected)
	}
	if err := ValidatePublicKnowledgeURL(ctx, p.lookup(), parsed); err != nil {
		return Document{}, err
	}
	invoker, err := p.NewFetch(parsed.String())
	if err != nil {
		return Document{}, err
	}
	payload, err := json.Marshal(map[string]any{
		"tool":      websearch.FetchTool,
		"arguments": map[string]any{"url": parsed.String()},
	})
	if err != nil {
		return Document{}, err
	}
	fetchedAt := time.Now().UnixMilli()
	envelope, err := p.invoke(ctx, invoker, payload)
	if err != nil {
		if errors.Is(err, ErrPluginCapabilityUnavailable) || errors.Is(err, plugin.ErrCapabilityDenied) {
			return Document{}, ErrPluginCapabilityUnavailable
		}
		return Document{}, fmt.Errorf("%w: %v", ErrFetchTransient, err)
	}
	result, err := websearch.DecodeFetchResult(envelope.Payload)
	if err != nil {
		return Document{}, fmt.Errorf("web fetch plugin result: %w", err)
	}
	if result.Status == 429 || result.Status >= 500 {
		return Document{}, fmt.Errorf("%w: upstream status %d", ErrFetchTransient, result.Status)
	}
	if result.Status < 200 || result.Status >= 300 {
		return Document{}, fmt.Errorf("%w: upstream status %d", ErrFetchRejected, result.Status)
	}
	mediaType, _, err := mime.ParseMediaType(result.ContentType)
	if err != nil || mediaType != "text/html" && mediaType != "text/plain" {
		return Document{}, fmt.Errorf("%w: unsupported content type", ErrFetchRejected)
	}
	if len(result.Body) > DocumentFetchMaxBodyBytes {
		return Document{}, fmt.Errorf("%w: body limit exceeded", ErrFetchRejected)
	}
	text, err := cleanKnowledgeDocument(mediaType, []byte(result.Body))
	if err != nil {
		return Document{}, err
	}
	contentSum := sha256.Sum256([]byte(text))
	contentHash := fmt.Sprintf("%x", contentSum[:])
	evidenceSum := sha256.Sum256([]byte(parsed.String() + "\x00" + contentHash))
	return Document{
		SourceID: source.ID, CanonicalURL: parsed.String(), Title: source.Title,
		Content: text, ContentHash: contentHash,
		EvidenceID:      fmt.Sprintf("web-evidence-%x", evidenceSum[:12]),
		ContentType:     mediaType,
		FetchedAtUnixMS: fetchedAt,
	}, nil
}

func (p *PluginTools) invoke(ctx context.Context, invoker EnvelopeInvoker, payload json.RawMessage) (plugin.Envelope, error) {
	if invoker == nil {
		return plugin.Envelope{}, ErrPluginCapabilityUnavailable
	}
	raw, err := sdk.Encode(plugin.Envelope{
		ABIVersion:  plugin.ABIVersion,
		Kind:        "handle",
		Correlation: plugin.Correlation{PluginInstanceID: p.InstanceID},
		Payload:     payload,
	})
	if err != nil {
		return plugin.Envelope{}, err
	}
	out, err := invoker(ctx, raw)
	if err != nil {
		if errors.Is(err, plugin.ErrCapabilityDenied) {
			return plugin.Envelope{}, ErrPluginCapabilityUnavailable
		}
		return plugin.Envelope{}, err
	}
	envelope, err := sdk.Decode(out)
	if err != nil {
		return plugin.Envelope{}, err
	}
	if envelope.Kind == "error" {
		if envelope.Error != nil && envelope.Error.Code == plugin.CodeCapabilityDenied {
			return plugin.Envelope{}, ErrPluginCapabilityUnavailable
		}
		if envelope.Error != nil {
			return plugin.Envelope{}, envelope.Error
		}
		return plugin.Envelope{}, ErrPluginCapabilityUnavailable
	}
	if envelope.Kind != "result" {
		return plugin.Envelope{}, errors.New("web plugin returned an invalid envelope")
	}
	return envelope, nil
}

func (p *PluginTools) lookup() knowledgeResolver {
	if p != nil && p.Resolver != nil {
		return p.Resolver
	}
	return net.DefaultResolver
}

type UnavailableDocumentFetcher struct{}

func (UnavailableDocumentFetcher) FetchSource(context.Context, IngestSource) (Document, error) {
	return Document{}, ErrPluginCapabilityUnavailable
}
