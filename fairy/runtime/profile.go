package runtime

import (
	"fmt"
	"os"
	"strings"
)

const (
	EnvRuntimeProfile          = "FAIRY_RUNTIME_PROFILE"
	ProfileFull        Profile = "full"
	ProfileDesktopLite Profile = "desktop-lite"
)

// Profile selects production dependency strictness.
type Profile string

func ParseProfile(raw string) (Profile, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return ProfileFull, nil
	}
	switch Profile(value) {
	case ProfileFull, ProfileDesktopLite:
		return Profile(value), nil
	default:
		return "", fmt.Errorf("FAIRY_RUNTIME_PROFILE must be %q or %q", ProfileFull, ProfileDesktopLite)
	}
}

func ProfileFromEnv(getenv func(string) string) (Profile, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	return ParseProfile(getenv(EnvRuntimeProfile))
}

func (p Profile) RequiresVectorIndex() bool {
	return p != ProfileDesktopLite
}
