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
