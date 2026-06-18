package transport

import "testing"

func TestNewLimiterDisabledWhenQPSZero(t *testing.T) {
	if newLimiter(0, 5) != nil {
		t.Error("qps=0 should disable the limiter (nil)")
	}
	if newLimiter(-1, 5) != nil {
		t.Error("negative qps should disable the limiter (nil)")
	}
}

func TestNewLimiterConfig(t *testing.T) {
	l := newLimiter(10, 5)
	if l == nil {
		t.Fatal("expected a limiter")
	}
	if float64(l.Limit()) != 10 {
		t.Errorf("Limit = %v, want 10", l.Limit())
	}
	if l.Burst() != 5 {
		t.Errorf("Burst = %d, want 5", l.Burst())
	}
}

func TestNewLimiterDefaultBurst(t *testing.T) {
	// Burst < 1 defaults to ceil(qps), min 1.
	if b := newLimiter(3.2, 0).Burst(); b != 4 {
		t.Errorf("default burst = %d, want 4 (ceil 3.2)", b)
	}
}

func TestLimiterThrottles(t *testing.T) {
	// burst=2 → first two tokens available immediately, third throttled.
	l := newLimiter(1, 2)
	if !l.Allow() || !l.Allow() {
		t.Fatal("first two requests within burst should be allowed")
	}
	if l.Allow() {
		t.Error("third immediate request should be throttled (no tokens left)")
	}
}
