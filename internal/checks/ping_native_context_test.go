package checks

import (
	"context"
	"testing"
	"time"
)

func TestNativePingCancellationReturnsBeforeProbeFinishes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	cleanedUp := make(chan struct{})
	returned := make(chan nativePingResult, 1)
	go func() {
		var result nativePingResult
		result.success, result.latencyMS, result.errorCategory, result.errorMessage, result.handled = runNativePingContext(ctx, func() (bool, *float64, string, string, bool) {
			defer close(cleanedUp)
			close(started)
			<-release
			latency := 1.0
			return true, &latency, "ok", "", true
		})
		returned <- result
	}()
	<-started
	cancel()
	select {
	case result := <-returned:
		if result.success || result.latencyMS != nil || result.errorCategory != "canceled" || !result.handled {
			t.Fatalf("expected canceled probe, got %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation waited for the native probe")
	}
	select {
	case <-cleanedUp:
		t.Fatal("probe resources were released before the native call finished")
	default:
	}
	// The native call can finish after the canceled caller has returned.
	release <- struct{}{}
	select {
	case <-cleanedUp:
	case <-time.After(time.Second):
		t.Fatal("native probe failed to clean up after cancellation")
	}
}

func TestNativePingDoesNotStartAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ok, _, category, _, handled := runNativePingContext(ctx, func() (bool, *float64, string, string, bool) {
		panic("canceled probe should not start")
	})
	if ok || category != "canceled" || !handled {
		t.Fatalf("expected cancellation without a probe: ok=%v category=%q handled=%v", ok, category, handled)
	}
}

func TestNativePingContextPreservesProbeResult(t *testing.T) {
	ok, latency, category, message, handled := runNativePingContext(context.Background(), func() (bool, *float64, string, string, bool) {
		return false, nil, "timeout", "Request timed out", true
	})
	if ok || latency != nil || category != "timeout" || message != "Request timed out" || !handled {
		t.Fatalf("native failure changed: ok=%v latency=%v category=%q message=%q handled=%v", ok, latency, category, message, handled)
	}
}

func TestNativePingContextRecoversProbePanic(t *testing.T) {
	ok, _, category, message, handled := runNativePingContext(context.Background(), func() (bool, *float64, string, string, bool) {
		panic("native failure")
	})
	if ok || category != "internal_error" || message != "native failure" || !handled {
		t.Fatalf("expected contained native panic: ok=%v category=%q message=%q handled=%v", ok, category, message, handled)
	}
}
