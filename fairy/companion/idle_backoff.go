package companion

import "time"

const (
	idleBackoffStartCount = 2
	idleBackoffBase       = 5 * time.Second
	idleBackoffCap        = 2 * time.Minute
)

func idleBackoffDelay(consecutiveSilent int) time.Duration {
	if consecutiveSilent < idleBackoffStartCount {
		return 0
	}
	exponent := consecutiveSilent - idleBackoffStartCount
	delay := idleBackoffBase
	for i := 0; i < exponent; i++ {
		if delay >= idleBackoffCap/2 {
			return idleBackoffCap
		}
		delay *= 2
	}
	if delay > idleBackoffCap {
		return idleBackoffCap
	}
	return delay
}
