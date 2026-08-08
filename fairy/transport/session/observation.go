package session

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxDesktopObservationIDRunes = 128
	MaxDesktopActivityRunes      = 64
	MaxDesktopLifecycleRunes     = 32
	MaxDesktopObservationAge     = 10 * time.Minute
)

type DesktopActivityCategory string

const (
	DesktopActivityUnknown   DesktopActivityCategory = "unknown"
	DesktopActivityWorking   DesktopActivityCategory = "working"
	DesktopActivityReading   DesktopActivityCategory = "reading"
	DesktopActivityCommuning DesktopActivityCategory = "communicating"
	DesktopActivityIdle      DesktopActivityCategory = "idle"
)

type DesktopLifecycleEvent string

const (
	DesktopLifecycleNone       DesktopLifecycleEvent = "none"
	DesktopLifecycleReturned   DesktopLifecycleEvent = "returned"
	DesktopLifecycleLeft       DesktopLifecycleEvent = "left"
	DesktopLifecyclePrivacyOn  DesktopLifecycleEvent = "privacy_on"
	DesktopLifecyclePrivacyOff DesktopLifecycleEvent = "privacy_off"
)

type DesktopPrivacyState string

const (
	DesktopPrivacyNormal       DesktopPrivacyState = "normal"
	DesktopPrivacyLocked       DesktopPrivacyState = "locked"
	DesktopPrivacyMeeting      DesktopPrivacyState = "meeting"
	DesktopPrivacyDoNotDisturb DesktopPrivacyState = "do_not_disturb"
	DesktopPrivacyProtected    DesktopPrivacyState = "protected"
)

type DesktopObservationTrigger string

const (
	DesktopTriggerPeriodic  DesktopObservationTrigger = "periodic"
	DesktopTriggerLifecycle DesktopObservationTrigger = "lifecycle"
)

// DesktopObservation contains only coarse, user-approved facts. It is not a
// transcript message and must never carry window titles, screenshots or input.
type DesktopObservation struct {
	ObservationID   string                    `json:"observationId"`
	TimestampUnixMS int64                     `json:"timestampUnixMs"`
	Trigger         DesktopObservationTrigger `json:"trigger"`
	Activity        DesktopActivityCategory   `json:"activity"`
	Lifecycle       DesktopLifecycleEvent     `json:"lifecycle"`
	Privacy         DesktopPrivacyState       `json:"privacy"`
}

func (o DesktopObservation) Validate(now time.Time) error {
	if strings.TrimSpace(o.ObservationID) == "" || utf8.RuneCountInString(o.ObservationID) > MaxDesktopObservationIDRunes {
		return errors.New("desktop observation id is invalid")
	}
	if o.TimestampUnixMS <= 0 || now.UnixMilli()-o.TimestampUnixMS > MaxDesktopObservationAge.Milliseconds() {
		return errors.New("desktop observation is stale or timestamp is invalid")
	}
	if o.Trigger != DesktopTriggerPeriodic && o.Trigger != DesktopTriggerLifecycle {
		return fmt.Errorf("desktop observation trigger is invalid: %q", o.Trigger)
	}
	if utf8.RuneCountInString(string(o.Activity)) > MaxDesktopActivityRunes || utf8.RuneCountInString(string(o.Lifecycle)) > MaxDesktopLifecycleRunes {
		return errors.New("desktop observation category is too long")
	}
	switch o.Privacy {
	case DesktopPrivacyNormal, DesktopPrivacyLocked, DesktopPrivacyMeeting, DesktopPrivacyDoNotDisturb, DesktopPrivacyProtected:
	default:
		return fmt.Errorf("desktop observation privacy is invalid: %q", o.Privacy)
	}
	return nil
}
