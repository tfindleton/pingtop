package pingtop

import (
	"sync"
	"testing"
	"time"
)

func TestRollingWindowCounterPrunesOutOfOrderResults(t *testing.T) {
	counter := NewRollingWindowCounter(30)
	ok := CheckResult{PingSuccess: true, ErrorCategory: "ok"}
	failure := CheckResult{ErrorCategory: "timeout"}
	// Concurrent checks and batch results need not arrive in timestamp order.
	counter.Observe(time.Unix(40, 0), ok)
	counter.Observe(time.Unix(5, 0), failure)
	counter.Observe(time.Unix(40, 0), ok)
	if got := counter.Snapshot(time.Unix(41, 0)); got != (CounterSummary{Checks: 2, Successes: 2}) {
		t.Fatalf("expired failure was retained behind newer results: %+v", got)
	}
}

func TestStateStoreKeepsFailureTotalsAfterRecoveryAndWindowExpiry(t *testing.T) {
	config := defaultConfig()
	config.Targets = []TargetSpec{{Value: "1.1.1.1", Kind: "ip"}}
	store := NewStateStore(config)
	old := time.Now().Add(-2 * time.Duration(config.StatsWindowSeconds) * time.Second)
	store.HandleCycle([]CheckResult{{Target: "1.1.1.1", TargetType: "ip", Timestamp: old, ErrorCategory: "timeout"}}, config, 1)
	store.HandleCycle([]CheckResult{{Target: "1.1.1.1", TargetType: "ip", Timestamp: time.Now(), PingSuccess: true, ErrorCategory: "ok"}}, config, 2)
	snapshot := store.Snapshot()
	stats := snapshot.TargetStats[0]
	if stats.FailureCount != 1 || snapshot.Session.Failures != 1 || stats.ConsecutiveFailures != 0 {
		t.Fatalf("recovery must clear only the streak: stats=%+v session=%+v", stats, snapshot.Session)
	}
	if stats.WindowSummary.Failures != 0 || snapshot.SessionWindow.Failures != 0 {
		t.Fatalf("old failure should expire from rolling counters: %+v", snapshot)
	}
	store.ResetCounters()
	snapshot = store.Snapshot()
	if snapshot.TargetStats[0].FailureCount != 0 || snapshot.Session.Failures != 0 {
		t.Fatalf("explicit reset should clear failures: %+v", snapshot)
	}
}

func TestStateStoreConcurrentSnapshotsPruneSafely(t *testing.T) {
	config := defaultConfig()
	config.Targets = []TargetSpec{{Value: "1.1.1.1", Kind: "ip"}}
	for iteration := 0; iteration < 20; iteration++ {
		store := NewStateStore(config)
		store.HandleCycle([]CheckResult{{
			Target: "1.1.1.1", TargetType: "ip", Timestamp: time.Unix(1, 0), ErrorCategory: "timeout",
		}}, config, 1)
		start := make(chan struct{})
		var workers sync.WaitGroup
		for index := 0; index < 16; index++ {
			workers.Add(1)
			go func() {
				defer workers.Done()
				<-start
				snapshot := store.Snapshot()
				if snapshot.SessionWindow != (CounterSummary{}) || snapshot.TargetStats[0].WindowSummary != (CounterSummary{}) {
					t.Errorf("concurrent expiration corrupted counters: %+v", snapshot)
				}
			}()
		}
		close(start)
		workers.Wait()
	}
}

func TestTargetStatsRetainsLatestFailureAfterRecovery(t *testing.T) {
	for _, category := range []string{"dns_failure", "timeout"} {
		t.Run(category, func(t *testing.T) {
			stats := TargetStats{Target: "example.test", TargetType: "hostname"}
			first := CheckResult{
				Timestamp: time.Unix(100, 0), ErrorCategory: category, ErrorMessage: "first failure",
			}
			if category == "dns_failure" {
				first.DNSSuccess = new(bool)
			}
			stats.Apply(first)
			recovered := CheckResult{Timestamp: time.Unix(101, 0), PingSuccess: true, ErrorCategory: "ok"}
			stats.Apply(recovered)
			if !stats.LastFailureAt.Equal(first.Timestamp) || stats.LastFailureCategory != category || stats.LastFailureMessage != first.ErrorMessage {
				t.Fatalf("successful check cleared failure history: %+v", stats)
			}
			if stats.LastState != "up" || stats.LastErrorCategory != "ok" || stats.LastErrorMessage != "" || !stats.LastCheckedAt.Equal(recovered.Timestamp) {
				t.Fatalf("retained failure changed latest-check state: %+v", stats)
			}

			later := CheckResult{Timestamp: time.Unix(102, 0), ErrorCategory: "ping_failure", ErrorMessage: "later failure"}
			stats.Apply(later)
			if !stats.LastFailureAt.Equal(later.Timestamp) || stats.LastFailureCategory != later.ErrorCategory || stats.LastFailureMessage != later.ErrorMessage {
				t.Fatalf("later failure did not replace retained history: %+v", stats)
			}
			stats.ResetCounters()
			if !stats.LastFailureAt.IsZero() || stats.LastFailureCategory != "" || stats.LastFailureMessage != "" || stats.FailureCount != 0 {
				t.Fatalf("explicit reset did not clear failure history: %+v", stats)
			}
			if stats.LastErrorCategory != later.ErrorCategory || stats.LastErrorMessage != later.ErrorMessage || !stats.LastCheckedAt.Equal(later.Timestamp) {
				t.Fatalf("counter reset changed latest-check details: %+v", stats)
			}
		})
	}
}

func TestStateStoreRetainsFailureHistoryWithLoggingOff(t *testing.T) {
	config := defaultConfig()
	config.Targets = []TargetSpec{{Value: "1.1.1.1", Kind: "ip"}}
	config.LoggingMode = LoggingModeOff
	store := NewStateStore(config)
	failure := CheckResult{
		Target: "1.1.1.1", TargetType: "ip", Timestamp: time.Now().Add(-2 * time.Duration(config.StatsWindowSeconds) * time.Second),
		ErrorCategory: "timeout", ErrorMessage: "ping timed out",
	}
	store.HandleCycle([]CheckResult{failure}, config, 1)
	store.HandleCycle([]CheckResult{{
		Target: failure.Target, TargetType: failure.TargetType, Timestamp: time.Now(), PingSuccess: true, ErrorCategory: "ok",
	}}, config, 2)
	for _, disabled := range []bool{true, false} {
		config.Targets[0].Disabled = disabled
		store.SyncTargets(config)
		snapshot := store.Snapshot()
		stats := snapshot.TargetStats[0]
		if !stats.LastFailureAt.Equal(failure.Timestamp) || stats.LastFailureCategory != failure.ErrorCategory || stats.LastFailureMessage != failure.ErrorMessage {
			t.Fatalf("logging off or enable/disable lost failure history: %+v", stats)
		}
		if stats.WindowSummary.Failures != 0 || stats.FailureCount != 1 || stats.LastErrorCategory != "ok" {
			t.Fatalf("failure history changed rolling/session/latest-check semantics: %+v", stats)
		}
		if len(snapshot.RecentEvents) != 0 {
			t.Fatalf("logging off unexpectedly emitted failure history events: %#v", snapshot.RecentEvents)
		}
	}
	store.ResetCounters()
	stats := store.Snapshot().TargetStats[0]
	if !stats.LastFailureAt.IsZero() || stats.LastFailureCategory != "" || stats.LastFailureMessage != "" {
		t.Fatalf("state-store reset did not clear retained failure: %+v", stats)
	}
}
