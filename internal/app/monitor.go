package app

import (
	"context"
	"fmt"
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
	workersStarted := false
	for {
		select {
		case <-monitor.stopCh:
			monitor.cancelActiveCycle()
			return
		default:
		}

		if monitor.IsPaused() {
			if workersStarted {
				monitor.cancelActiveCycle()
				monitor.stateStore.ClearActiveCycle()
				workersStarted = false
			}
			if monitor.waitForSignal(100*time.Millisecond) == monitorSignalStop {
				monitor.cancelActiveCycle()
				return
			}
			continue
		}

		config := monitor.configManager.Snapshot()
		generation := atomic.LoadInt64(&monitor.generation)
		monitor.startTargetWorkers(config, generation)
		workersStarted = true

		switch monitor.waitForSignal(24 * time.Hour) {
		case monitorSignalStop:
			monitor.cancelActiveCycle()
			return
		case monitorSignalWake:
			continue
		}
	}
}

func (monitor *BackgroundMonitor) startTargetWorkers(config AppConfig, generation int64) {
	ctx, cancel := context.WithCancel(context.Background())
	monitor.cycleMu.Lock()
	previousCancel := monitor.cancelCycle
	monitor.cancelCycle = cancel
	monitor.cycleMu.Unlock()
	if previousCancel != nil {
		previousCancel()
	}

	monitor.stateStore.SyncTargets(config)
	targets := config.EnabledTargets()
	if len(targets) == 0 {
		cycleID := int(atomic.AddInt32(&monitor.cycleID, 1))
		monitor.stateStore.HandleCycle(nil, config, cycleID)
		return
	}

	for index, target := range targets {
		go monitor.runTargetWorker(ctx, config, target, generation, index+1, fmt.Sprintf("target-%d", index+1))
	}
}

func (monitor *BackgroundMonitor) runTargetWorker(
	ctx context.Context,
	config AppConfig,
	target TargetSpec,
	generation int64,
	index int,
	workerID string,
) {
	interval := time.Duration(config.CheckIntervalSeconds * float64(time.Second))
	if interval <= 0 {
		interval = time.Second
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-monitor.stopCh:
			return
		default:
		}

		cycleID := int(atomic.AddInt32(&monitor.cycleID, 1))
		checkWorkerID := workerID
		if checkWorkerID == "" {
			checkWorkerID = fmt.Sprintf("target-%d", index)
		}
		monitor.stateStore.BeginTargetCheck(cycleID, generation, config, target, time.Now())
		result := monitor.coordinator.CheckTargetContext(ctx, target, config.PingTimeoutMS, cycleID, checkWorkerID)
		if ctx.Err() != nil || result.ErrorCategory == "canceled" {
			monitor.stateStore.FinishCycle(cycleID, generation)
			return
		}
		result.Sequence = atomic.AddInt64(&monitor.sequence, 1)
		monitor.stateStore.HandleTargetResult(result, config, cycleID, generation)
		monitor.logger.LogResults([]CheckResult{result}, config)

		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-monitor.stopCh:
			timer.Stop()
			return
		case <-timer.C:
		}
	}
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
