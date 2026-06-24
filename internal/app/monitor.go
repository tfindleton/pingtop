package app

import (
	"sync"
	"sync/atomic"
	"time"
)

type BackgroundMonitor struct {
	configManager *ConfigManager
	stateStore    *StateStore
	logger        *CSVLogger
	coordinator   *CheckCoordinator

	stopCh   chan struct{}
	wakeCh   chan struct{}
	doneCh   chan struct{}
	pauseMu  sync.RWMutex
	paused   bool
	sequence int64
	cycleID  int32
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
	<-monitor.doneCh
}

func (monitor *BackgroundMonitor) Wake() {
	select {
	case monitor.wakeCh <- struct{}{}:
	default:
	}
}

func (monitor *BackgroundMonitor) TogglePause() bool {
	monitor.pauseMu.Lock()
	defer monitor.pauseMu.Unlock()
	monitor.paused = !monitor.paused
	monitor.Wake()
	return monitor.paused
}

func (monitor *BackgroundMonitor) IsPaused() bool {
	monitor.pauseMu.RLock()
	defer monitor.pauseMu.RUnlock()
	return monitor.paused
}

func (monitor *BackgroundMonitor) RunSingleCycle(config AppConfig) []CheckResult {
	cycleID := int(atomic.AddInt32(&monitor.cycleID, 1))
	results := monitor.coordinator.ExecuteCycle(config, cycleID)
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
		results := monitor.RunSingleCycle(config)
		monitor.stateStore.HandleCycle(results, config, monitor.CurrentCycleID())
		monitor.logger.LogResults(results, config)
		nextRun = time.Now().Add(time.Duration(config.CheckIntervalSeconds * float64(time.Second)))
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
