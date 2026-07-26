package proactive

import (
	"testing"
	"time"

	"fairy/contracts/observation"
)

func TestEvidenceRegistryExpiresIDsWithoutRawData(t *testing.T) {
	now := time.UnixMilli(100000)
	r := NewEvidenceRegistry()
	obs := observation.DesktopObservation{
		ObservationID:   "obs-1",
		TimestampUnixMS: now.UnixMilli(),
		Trigger:         observation.DesktopTriggerPeriodic,
		Privacy:         observation.DesktopPrivacyNormal,
	}
	if err := r.Accept(obs, now); err != nil {
		t.Fatal(err)
	}
	if !r.ContainsFresh("obs-1", now.Add(time.Second)) {
		t.Fatal("accepted evidence missing")
	}
	if r.ContainsFresh("obs-1", now.Add(11*time.Minute)) {
		t.Fatal("expired evidence remained")
	}
}
