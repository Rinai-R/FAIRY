//go:build !darwin

package main

import (
	"context"
	"errors"
	"time"

	"fairy/transport/session"
)

type macOSIdleSampler struct{}

func newMacOSIdleSampler(time.Duration, func() session.DesktopPrivacyState) (*macOSIdleSampler, error) {
	return nil, errors.New("desktop observation sampler is unavailable on this platform")
}

func (*macOSIdleSampler) Sample(context.Context) (session.DesktopObservation, error) {
	return session.DesktopObservation{}, errors.New("desktop observation sampler is unavailable on this platform")
}
