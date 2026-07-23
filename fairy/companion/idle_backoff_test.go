package companion

import "testing"

func TestIdleBackoffDelayEscalatesThenCaps(t *testing.T) {
	if delay := idleBackoffDelay(1); delay != 0 {
		t.Fatalf("before start = %s", delay)
	}
	if delay := idleBackoffDelay(idleBackoffStartCount); delay != idleBackoffBase {
		t.Fatalf("start = %s", delay)
	}
	if delay := idleBackoffDelay(idleBackoffStartCount + 1); delay != idleBackoffBase*2 {
		t.Fatalf("second = %s", delay)
	}
	if delay := idleBackoffDelay(20); delay != idleBackoffCap {
		t.Fatalf("cap = %s", delay)
	}
}
