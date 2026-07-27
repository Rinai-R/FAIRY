//go:build darwin

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"sync"
	"time"

	"fairy/session"
)

var hidIdleTimePattern = regexp.MustCompile(`"HIDIdleTime"\s*=\s*([0-9]+)`)

type macOSIdleSampler struct {
	mu            sync.Mutex
	idleThreshold time.Duration
	privacy       func() session.DesktopPrivacyState
	run           func(context.Context) ([]byte, error)
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
		run: func(ctx context.Context) ([]byte, error) {
			return exec.CommandContext(ctx, "/usr/sbin/ioreg", "-c", "IOHIDSystem", "-r", "-d", "1").Output()
		},
	}, nil
}

func (s *macOSIdleSampler) Sample(ctx context.Context) (session.DesktopObservation, error) {
	output, err := s.run(ctx)
	if err != nil {
		return session.DesktopObservation{}, fmt.Errorf("reading macOS idle state: %w", err)
	}
	idle, err := parseMacOSHIDIdleTime(output)
	if err != nil {
		return session.DesktopObservation{}, err
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

func parseMacOSHIDIdleTime(output []byte) (time.Duration, error) {
	match := hidIdleTimePattern.FindSubmatch(output)
	if len(match) != 2 {
		return 0, errors.New("macOS HID idle time is unavailable")
	}
	nanoseconds, err := strconv.ParseUint(string(match[1]), 10, 64)
	if err != nil {
		return 0, errors.New("macOS HID idle time is invalid")
	}
	if nanoseconds > uint64(^uint64(0)>>1) {
		return 0, errors.New("macOS HID idle time exceeds duration range")
	}
	return time.Duration(nanoseconds), nil
}

func newDesktopObservationID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generating observation id: %w", err)
	}
	return "desktop-" + hex.EncodeToString(value[:]), nil
}
