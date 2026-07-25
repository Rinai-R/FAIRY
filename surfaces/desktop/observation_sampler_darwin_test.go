//go:build darwin

package main

import (
	"context"
	"testing"
	"time"

	obs "fairy/contracts/observation"
)

func TestMacOSIdleSamplerReportsReturnedWithoutSensitiveApplicationData(t *testing.T) {
	sampler, err := newMacOSIdleSampler(time.Minute, func() obs.DesktopPrivacyState { return obs.DesktopPrivacyNormal })
	if err != nil {
		t.Fatal(err)
	}
	sampler.run = func(context.Context) ([]byte, error) { return []byte(`"HIDIdleTime" = 120000000000`), nil }
	first, err := sampler.Sample(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.Activity != obs.DesktopActivityIdle || first.Lifecycle != obs.DesktopLifecycleNone {
		t.Fatalf("first = %#v", first)
	}
	sampler.run = func(context.Context) ([]byte, error) { return []byte(`"HIDIdleTime" = 1000000`), nil }
	returned, err := sampler.Sample(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if returned.Trigger != obs.DesktopTriggerLifecycle || returned.Lifecycle != obs.DesktopLifecycleReturned || returned.Activity != obs.DesktopActivityWorking {
		t.Fatalf("returned = %#v", returned)
	}
}

func TestParseMacOSHIDIdleTimeRejectsMissingValue(t *testing.T) {
	if _, err := parseMacOSHIDIdleTime([]byte("no idle value")); err == nil {
		t.Fatal("missing HID idle time error = nil")
	}
}

func TestMacOSIdleSamplerDowngradesPrivacyTransition(t *testing.T) {
	privacy := obs.DesktopPrivacyNormal
	sampler, err := newMacOSIdleSampler(time.Minute, func() obs.DesktopPrivacyState { return privacy })
	if err != nil {
		t.Fatal(err)
	}
	sampler.run = func(context.Context) ([]byte, error) { return []byte(`"HIDIdleTime" = 1000000`), nil }
	if _, err := sampler.Sample(context.Background()); err != nil {
		t.Fatal(err)
	}
	privacy = obs.DesktopPrivacyMeeting
	sample, err := sampler.Sample(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sample.Lifecycle != obs.DesktopLifecyclePrivacyOn || sample.Activity != obs.DesktopActivityUnknown || sample.Privacy != obs.DesktopPrivacyMeeting {
		t.Fatalf("privacy observation = %#v", sample)
	}
}
