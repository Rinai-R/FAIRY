package config

import fairyconfig "fairy/config"

type (
	ConfigService             = fairyconfig.ConfigService
	Reader                    = fairyconfig.Reader
	ModelConnection           = fairyconfig.ModelConnection
	ModelConnectionInput      = fairyconfig.ModelConnectionInput
	ModelConnectionStatus     = fairyconfig.ModelConnectionStatus
	SemanticEmbeddingSettings = fairyconfig.SemanticEmbeddingSettings
	SemanticEmbeddingStatus   = fairyconfig.SemanticEmbeddingStatus
	WebSearchSettings         = fairyconfig.WebSearchSettings
)

const (
	SemanticEmbeddingProviderNone             = fairyconfig.SemanticEmbeddingProviderNone
	SemanticEmbeddingProviderOpenAICompatible = fairyconfig.SemanticEmbeddingProviderOpenAICompatible
)

var (
	NewConfigService              = fairyconfig.NewConfigService
	NewReader                     = fairyconfig.NewReader
	ReadModelConnection           = fairyconfig.ReadModelConnection
	ReadModelConnectionStatus     = fairyconfig.ReadModelConnectionStatus
	ReadSemanticEmbeddingSettings = fairyconfig.ReadSemanticEmbeddingSettings
	ReadWebSearchSettings         = fairyconfig.ReadWebSearchSettings
)
