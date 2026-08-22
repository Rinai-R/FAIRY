//go:build darwin

package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"fairy/transport/session"
)

func TestMacOSIdleSamplerReportsReturnedWithoutSensitiveApplicationData(t *testing.T) {
	sampler, err := newMacOSIdleSampler(time.Minute, func() session.DesktopPrivacyState { return session.DesktopPrivacyNormal })
	if err != nil {
		t.Fatal(err)
	}
	sampler.readIdle = func(context.Context) (time.Duration, error) { return 2 * time.Minute, nil }
	first, err := sampler.Sample(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.Activity != session.DesktopActivityIdle || first.Lifecycle != session.DesktopLifecycleNone {
		t.Fatalf("first = %#v", first)
	}
	sampler.readIdle = func(context.Context) (time.Duration, error) { return time.Millisecond, nil }
	returned, err := sampler.Sample(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if returned.Trigger != session.DesktopTriggerLifecycle || returned.Lifecycle != session.DesktopLifecycleReturned || returned.Activity != session.DesktopActivityWorking {
		t.Fatalf("returned = %#v", returned)
	}
}

func TestMacOSIdleSamplerPropagatesReadFailure(t *testing.T) {
	sampler, err := newMacOSIdleSampler(time.Minute, func() session.DesktopPrivacyState { return session.DesktopPrivacyNormal })
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("idle API unavailable")
	sampler.readIdle = func(context.Context) (time.Duration, error) { return 0, want }
	if _, err := sampler.Sample(t.Context()); !errors.Is(err, want) {
		t.Fatalf("Sample() error = %v, want %v", err, want)
	}
}

func TestMacOSIdleSamplerDowngradesPrivacyTransition(t *testing.T) {
	privacy := session.DesktopPrivacyNormal
	sampler, err := newMacOSIdleSampler(time.Minute, func() session.DesktopPrivacyState { return privacy })
	if err != nil {
		t.Fatal(err)
	}
	sampler.readIdle = func(context.Context) (time.Duration, error) { return time.Millisecond, nil }
	if _, err := sampler.Sample(context.Background()); err != nil {
		t.Fatal(err)
	}
	privacy = session.DesktopPrivacyMeeting
	sample, err := sampler.Sample(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sample.Lifecycle != session.DesktopLifecyclePrivacyOn || sample.Activity != session.DesktopActivityUnknown || sample.Privacy != session.DesktopPrivacyMeeting {
		t.Fatalf("privacy observation = %#v", sample)
	}
}
