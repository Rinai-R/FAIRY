//go:build darwin

package main

import (
	"context"
	"testing"
	"time"

	"fairy/transport/session"
)

func TestMacOSIdleSamplerReportsReturnedWithoutSensitiveApplicationData(t *testing.T) {
	sampler, err := newMacOSIdleSampler(time.Minute, func() session.DesktopPrivacyState { return session.DesktopPrivacyNormal })
	if err != nil {
		t.Fatal(err)
	}
	sampler.run = func(context.Context) ([]byte, error) { return []byte(`"HIDIdleTime" = 120000000000`), nil }
	first, err := sampler.Sample(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.Activity != session.DesktopActivityIdle || first.Lifecycle != session.DesktopLifecycleNone {
		t.Fatalf("first = %#v", first)
	}
	sampler.run = func(context.Context) ([]byte, error) { return []byte(`"HIDIdleTime" = 1000000`), nil }
	returned, err := sampler.Sample(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if returned.Trigger != session.DesktopTriggerLifecycle || returned.Lifecycle != session.DesktopLifecycleReturned || returned.Activity != session.DesktopActivityWorking {
		t.Fatalf("returned = %#v", returned)
	}
}

func TestParseMacOSHIDIdleTimeRejectsMissingValue(t *testing.T) {
	if _, err := parseMacOSHIDIdleTime([]byte("no idle value")); err == nil {
		t.Fatal("missing HID idle time error = nil")
	}
}

func TestMacOSIdleSamplerDowngradesPrivacyTransition(t *testing.T) {
	privacy := session.DesktopPrivacyNormal
	sampler, err := newMacOSIdleSampler(time.Minute, func() session.DesktopPrivacyState { return privacy })
	if err != nil {
		t.Fatal(err)
	}
	sampler.run = func(context.Context) ([]byte, error) { return []byte(`"HIDIdleTime" = 1000000`), nil }
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
