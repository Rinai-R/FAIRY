//go:build darwin

package main

/*
#cgo LDFLAGS: -framework ApplicationServices
#include <ApplicationServices/ApplicationServices.h>

static double fairy_seconds_since_last_input(void) {
	return CGEventSourceSecondsSinceLastEventType(
		kCGEventSourceStateCombinedSessionState,
		kCGAnyInputEventType
	);
}
*/
import "C"

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"fairy/transport/session"
)

type macOSIdleSampler struct {
	mu            sync.Mutex
	idleThreshold time.Duration
	privacy       func() session.DesktopPrivacyState
	readIdle      func(context.Context) (time.Duration, error)
	wasIdle       bool
	lastPrivacy   session.DesktopPrivacyState
}

func newMacOSIdleSampler(idleThreshold time.Duration, privacy func() session.DesktopPrivacyState) (*macOSIdleSampler, error) {
	if idleThreshold <= 0 {
		return nil, errors.New("macOS observation idle threshold must be positive")
	}
	if privacy == nil {
		return nil, errors.New("macOS observation privacy provider is required")
	}
	return &macOSIdleSampler{
		idleThreshold: idleThreshold,
		privacy:       privacy,
		readIdle:      readMacOSIdleTime,
	}, nil
}

func (s *macOSIdleSampler) Sample(ctx context.Context) (session.DesktopObservation, error) {
	idle, err := s.readIdle(ctx)
	if err != nil {
		return session.DesktopObservation{}, fmt.Errorf("reading macOS idle state: %w", err)
	}
	now := time.Now()
	isIdle := idle >= s.idleThreshold
	privacy := s.privacy()
	s.mu.Lock()
	trigger := session.DesktopTriggerPeriodic
	lifecycle := session.DesktopLifecycleNone
	if s.lastPrivacy != "" && s.lastPrivacy != privacy {
		trigger = session.DesktopTriggerLifecycle
		if privacy == session.DesktopPrivacyNormal {
			lifecycle = session.DesktopLifecyclePrivacyOff
		} else {
			lifecycle = session.DesktopLifecyclePrivacyOn
		}
	} else if s.wasIdle && !isIdle {
		trigger = session.DesktopTriggerLifecycle
		lifecycle = session.DesktopLifecycleReturned
	}
	s.wasIdle = isIdle
	s.lastPrivacy = privacy
	s.mu.Unlock()
	activity := session.DesktopActivityWorking
	if isIdle {
		activity = session.DesktopActivityIdle
	}
	if privacy != session.DesktopPrivacyNormal {
		activity = session.DesktopActivityUnknown
	}
	id, err := newDesktopObservationID()
	if err != nil {
		return session.DesktopObservation{}, err
	}
	return session.DesktopObservation{
		ObservationID: id, TimestampUnixMS: now.UnixMilli(), Trigger: trigger,
		Activity: activity, Lifecycle: lifecycle, Privacy: privacy,
	}, nil
}

func readMacOSIdleTime(ctx context.Context) (time.Duration, error) {
	if ctx == nil {
		return 0, errors.New("macOS idle sampler context is required")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	seconds := float64(C.fairy_seconds_since_last_input())
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 {
		return 0, errors.New("macOS idle time is unavailable")
	}
	maxSeconds := float64(math.MaxInt64) / float64(time.Second)
	if seconds > maxSeconds {
		return 0, errors.New("macOS idle time exceeds duration range")
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

func newDesktopObservationID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generating observation id: %w", err)
	}
	return "desktop-" + hex.EncodeToString(value[:]), nil
}
