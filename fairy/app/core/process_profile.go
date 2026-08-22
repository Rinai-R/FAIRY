package core

import (
	"fmt"
	"os"
	"strings"
)

const (
	EnvRuntimeProfile             = "FAIRY_RUNTIME_PROFILE"
	ProfileFull           Profile = "full"
	ProfileDesktopLite    Profile = "desktop-lite"
	ProfileEndpointStrict Profile = "endpoint-strict"
)

// Profile selects production dependency strictness.
type Profile string

func ParseProfile(raw string) (Profile, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return ProfileFull, nil
	}
	switch Profile(value) {
	case ProfileFull, ProfileDesktopLite, ProfileEndpointStrict:
		return Profile(value), nil
	default:
		return "", fmt.Errorf("FAIRY_RUNTIME_PROFILE must be %q, %q, or %q", ProfileFull, ProfileDesktopLite, ProfileEndpointStrict)
	}
}

func ProfileFromEnv(getenv func(string) string) (Profile, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	return ParseProfile(getenv(EnvRuntimeProfile))
}
