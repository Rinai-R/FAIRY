package memory

import (
	"errors"
)

var ErrEndpointBindingMismatch = errors.New("endpoint conversation binding does not match stored interaction facts")

const (
	maxSocialPersonNotes      = 8
	recentSocialFeedbackLimit = 12
	MaxMessagePageLimit       = 200
	DefaultMessagePageLimit   = 50

	maxCompanionPortraitMemories   = 6
	maxCompanionPortraitPerKind    = 2
	maxCompanionPortraitRunes      = 1200
	maxCompanionPortraitCandidates = 16

	maxSocialQueryFragments = 16
	maxSocialQueryRunes     = 256
	socialQueryWindowRunes  = 4
)
