//go:build !darwin

package main

import (
	"context"
	"errors"
	"time"

	obs "fairy/contracts/observation"
)

type macOSIdleSampler struct{}

func newMacOSIdleSampler(time.Duration, func() obs.DesktopPrivacyState) (*macOSIdleSampler, error) {
	return nil, errors.New("desktop observation sampler is unavailable on this platform")
}

func (*macOSIdleSampler) Sample(context.Context) (obs.DesktopObservation, error) {
	return obs.DesktopObservation{}, errors.New("desktop observation sampler is unavailable on this platform")
}
