package presence

import (
	"time"

	history "fairy/context/history/transcript"
)

const (
	participationQuietPeriod  = time.Second
	participationMaximumWait  = 12 * time.Second
	participationBasePressure = 2
	participationMaxPressure  = 7
)

type participationSchedule struct {
	PendingCount        int
	PressureThreshold   int
	PendingFor          time.Duration
	AssistantReplies5m  uint64
	AssistantReplies30m uint64
	UserMessages30m     uint64
	Ready               bool
}

func deriveParticipationSchedule(pendingCount int, pendingSince, now time.Time, activity history.ConversationActivity) participationSchedule {
	pendingFor := time.Duration(0)
	if !pendingSince.IsZero() && now.After(pendingSince) {
		pendingFor = now.Sub(pendingSince)
	}
	threshold := participationPressureThreshold(activity)
	return participationSchedule{
		PendingCount:        pendingCount,
		PressureThreshold:   threshold,
		PendingFor:          pendingFor,
		AssistantReplies5m:  activity.AssistantMessages5Minutes,
		AssistantReplies30m: activity.AssistantMessages30Minutes,
		UserMessages30m:     activity.UserMessages30Minutes,
		Ready:               pendingCount >= threshold || pendingFor >= participationMaximumWait,
	}
}

func participationPressureThreshold(activity history.ConversationActivity) int {
	threshold := participationBasePressure + min(int(activity.AssistantMessages5Minutes), 3)
	total := activity.AssistantMessages30Minutes + activity.UserMessages30Minutes
	if total > 0 {
		share := float64(activity.AssistantMessages30Minutes) / float64(total)
		switch {
		case share > 0.55:
			threshold += 2
		case share > 0.25:
			threshold++
		}
	}
	return min(threshold, participationMaxPressure)
}

func participationScheduleDelay(now, pendingSince, lastReceived, backoffUntil time.Time) time.Duration {
	quietDeadline := lastReceived.Add(participationQuietPeriod)
	maximumDeadline := pendingSince.Add(participationMaximumWait)
	deadline := minTime(quietDeadline, maximumDeadline)
	if backoffUntil.After(deadline) {
		deadline = backoffUntil
	}
	if !deadline.After(now) {
		return 0
	}
	return deadline.Sub(now)
}

func minTime(left, right time.Time) time.Time {
	if left.Before(right) {
		return left
	}
	return right
}
