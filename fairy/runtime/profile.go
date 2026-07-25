package runtime

import "fairy/internal/bootstrap"

const (
	EnvRuntimeProfile          = bootstrap.EnvRuntimeProfile
	ProfileFull        Profile = bootstrap.ProfileFull
	ProfileDesktopLite Profile = bootstrap.ProfileDesktopLite
)

type Profile = bootstrap.Profile

var (
	ParseProfile   = bootstrap.ParseProfile
	ProfileFromEnv = bootstrap.ProfileFromEnv
)
