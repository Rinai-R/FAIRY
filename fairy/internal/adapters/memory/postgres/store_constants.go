package postgres

import (
	"errors"

	domainmemory "fairy/internal/domain/memory"
)

var ErrEndpointBindingMismatch = errors.New("endpoint conversation binding does not match stored interaction facts")

const (
	SocialNegativeSuppressThreshold = domainmemory.SocialNegativeSuppressThreshold
	DefaultExtractionBatchLimit     = domainmemory.DefaultExtractionBatchLimit
	MaxMemoryMutationsPerBatch      = domainmemory.MaxMemoryMutationsPerBatch
	maxSocialPersonNotes            = 8
	recentSocialFeedbackLimit       = 12
	MaxMessagePageLimit             = 200

	maxCompanionPortraitMemories   = 6
	maxCompanionPortraitPerKind    = 2
	maxCompanionPortraitRunes      = 1200
	maxCompanionPortraitCandidates = 16

	maxSocialQueryFragments = 16
	maxSocialQueryRunes     = 256
	socialQueryWindowRunes  = 4
)
