package app

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

type BackgroundMonitor struct {
	configManager *ConfigManager
	stateStore    *StateStore
	logger        *CSVLogger
	coordinator   *CheckCoordinator

	stopCh      chan struct{}
	wakeCh      chan struct{}
	doneCh      chan struct{}
	pauseMu     sync.RWMutex
	paused      bool
	sequence    int64
	cycleID     int32
	generation  int64
	cycleMu     sync.Mutex
	cancelCycle context.CancelFunc
}

type monitorSignal int

const (
	monitorSignalTimer monitorSignal = iota
	monitorSignalWake
	monitorSignalStop
)

func NewBackgroundMonitor(
	configManager *ConfigManager,
	stateStore *StateStore,
	logger *CSVLogger,
	coordinator *CheckCoordinator,
) *BackgroundMonitor {
	return &BackgroundMonitor{
		configManager: configManager,
		stateStore:    stateStore,
		logger:        logger,
		coordinator:   coordinator,
		stopCh:        make(chan struct{}),
		wakeCh:        make(chan struct{}, 1),
		doneCh:        make(chan struct{}),
	}
}

func (monitor *BackgroundMonitor) Start() {
	go monitor.run()
}

func (monitor *BackgroundMonitor) Stop() {
	select {
	case <-monitor.stopCh:
	default:
		close(monitor.stopCh)
	}
	monitor.cancelActiveCycle()
	<-monitor.doneCh
}

func (monitor *BackgroundMonitor) Wake() {
	select {
	case monitor.wakeCh <- struct{}{}:
	default:
	}
}

func (monitor *BackgroundMonitor) ForceRefresh() {
	atomic.AddInt64(&monitor.generation, 1)
	monitor.cancelActiveCycle()
	monitor.stateStore.ClearActiveCycle()
	monitor.Wake()
}

func (monitor *BackgroundMonitor) TogglePause() bool {
	monitor.pauseMu.Lock()
	defer monitor.pauseMu.Unlock()
	monitor.paused = !monitor.paused
	if monitor.paused {
		monitor.ForceRefresh()
	} else {
		monitor.Wake()
	}
	return monitor.paused
}

func (monitor *BackgroundMonitor) IsPaused() bool {
	monitor.pauseMu.RLock()
	defer monitor.pauseMu.RUnlock()
	return monitor.paused
}

func (monitor *BackgroundMonitor) RunSingleCycle(config AppConfig) []CheckResult {
	cycleID := int(atomic.AddInt32(&monitor.cycleID, 1))
	results := monitor.coordinator.ExecuteCycleContext(context.Background(), config, cycleID, nil)
	monitor.stampSequences(results)
	return results
}

func (monitor *BackgroundMonitor) CurrentCycleID() int {
	return int(atomic.LoadInt32(&monitor.cycleID))
}

func (monitor *BackgroundMonitor) run() {
	defer close(monitor.doneCh)
	nextRun := time.Now()
	for {
		select {
		case <-monitor.stopCh:
			return
		default:
		}

		if monitor.IsPaused() {
			nextRun = time.Now()
			if monitor.waitForSignal(100*time.Millisecond) == monitorSignalStop {
				return
			}
			continue
		}

		now := time.Now()
		if now.Before(nextRun) {
			switch monitor.waitForSignal(nextRun.Sub(now)) {
			case monitorSignalStop:
				return
			case monitorSignalWake:
				nextRun = time.Now()
			}
			continue
		}

		config := monitor.configManager.Snapshot()
		cycleID := int(atomic.AddInt32(&monitor.cycleID, 1))
		generation := atomic.LoadInt64(&monitor.generation)
		ctx, cancel := monitor.newCycleContext()
		monitor.stateStore.BeginCycle(cycleID, generation, config, time.Now())
		results := monitor.coordinator.ExecuteCycleContext(ctx, config, cycleID, func(result CheckResult) {
			monitor.stateStore.NoteCycleProgress(cycleID, generation, result)
		})
		cycleCanceled := ctx.Err() != nil
		monitor.clearCycleContext(cancel)
		if cycleCanceled || generation != atomic.LoadInt64(&monitor.generation) {
			monitor.stateStore.FinishCycle(cycleID, generation)
			nextRun = time.Now()
			continue
		}
		monitor.stampSequences(results)
		monitor.stateStore.HandleCycle(results, config, cycleID)
		monitor.logger.LogResults(results, config)
		nextRun = time.Now().Add(time.Duration(config.CheckIntervalSeconds * float64(time.Second)))
	}
}

func (monitor *BackgroundMonitor) newCycleContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	monitor.cycleMu.Lock()
	monitor.cancelCycle = cancel
	monitor.cycleMu.Unlock()
	return ctx, cancel
}

func (monitor *BackgroundMonitor) clearCycleContext(cancel context.CancelFunc) {
	monitor.cycleMu.Lock()
	if monitor.cancelCycle != nil {
		monitor.cancelCycle = nil
	}
	monitor.cycleMu.Unlock()
	cancel()
}

func (monitor *BackgroundMonitor) cancelActiveCycle() {
	monitor.cycleMu.Lock()
	cancel := monitor.cancelCycle
	monitor.cycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (monitor *BackgroundMonitor) waitForSignal(duration time.Duration) monitorSignal {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-monitor.stopCh:
		return monitorSignalStop
	case <-monitor.wakeCh:
		return monitorSignalWake
	case <-timer.C:
		return monitorSignalTimer
	}
}

func (monitor *BackgroundMonitor) stampSequences(results []CheckResult) {
	for index := range results {
		results[index].Sequence = atomic.AddInt64(&monitor.sequence, 1)
	}
}
