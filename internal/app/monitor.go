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

	stopCh       chan struct{}
	wakeCh       chan struct{}
	doneCh       chan struct{}
	pauseMu      sync.RWMutex
	paused       bool
	sequence     atomic.Int64
	cycleID      atomic.Int32
	generation   atomic.Int64
	cycleMu      sync.Mutex
	cancelCycle  context.CancelFunc
	workers      sync.WaitGroup
	logMu        sync.Mutex
	lastLogError string
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
	monitor.cycleMu.Lock()
	monitor.invalidateWorkersLocked()
	monitor.cycleMu.Unlock()
	monitor.Wake()
}

// Keep state synchronization and worker cancellation atomic with result updates.
// Otherwise a finishing worker could restore the configuration it started with.
func (monitor *BackgroundMonitor) updateConfig(update func(*AppConfig)) (AppConfig, bool) {
	monitor.cycleMu.Lock()
	config := monitor.configManager.Update(update)
	windowReset := monitor.stateStore.SyncTargets(config)
	monitor.invalidateWorkersLocked()
	monitor.cycleMu.Unlock()
	monitor.Wake()
	return config, windowReset
}

func (monitor *BackgroundMonitor) updateLoggingMode(mode string) {
	monitor.logMu.Lock()
	defer monitor.logMu.Unlock()
	config, _ := monitor.updateConfig(func(config *AppConfig) {
		config.LoggingMode = mode
	})
	// Apply mode changes even when paused or all targets are disabled. In
	// particular, switching off must immediately discard queued log history.
	monitor.logResultsLocked(nil, config)
}

func (monitor *BackgroundMonitor) invalidateWorkersLocked() {
	monitor.generation.Add(1)
	if monitor.cancelCycle != nil {
		monitor.cancelCycle()
	}
	monitor.stateStore.ClearActiveCycle()
}

func (monitor *BackgroundMonitor) TogglePause() bool {
	monitor.pauseMu.Lock()
	monitor.paused = !monitor.paused
	paused := monitor.paused
	monitor.pauseMu.Unlock()
	if paused {
		monitor.ForceRefresh()
	} else {
		monitor.Wake()
	}
	return paused
}

func (monitor *BackgroundMonitor) IsPaused() bool {
	monitor.pauseMu.RLock()
	defer monitor.pauseMu.RUnlock()
	return monitor.paused
}

func (monitor *BackgroundMonitor) RunSingleCycle(config AppConfig) []CheckResult {
	return monitor.RunSingleCycleContext(context.Background(), config)
}

func (monitor *BackgroundMonitor) RunSingleCycleContext(ctx context.Context, config AppConfig) []CheckResult {
	cycleID := int(monitor.cycleID.Add(1))
	results := monitor.coordinator.ExecuteCycleContext(ctx, config, cycleID, nil)
	monitor.stampSequences(results)
	return results
}

func (monitor *BackgroundMonitor) CurrentCycleID() int {
	return int(monitor.cycleID.Load())
}

func (monitor *BackgroundMonitor) recordSingleCycle(results []CheckResult, config AppConfig, interrupted bool) error {
	cycleID := monitor.CurrentCycleID()
	if interrupted {
		// Preserve completed checks without treating the partial set as a complete
		// cycle or using it to produce a network-wide diagnosis.
		for _, result := range results {
			monitor.stateStore.HandleTargetResult(result, config, cycleID, 0)
		}
	} else {
		monitor.stateStore.HandleCycle(results, config, cycleID)
	}
	return monitor.logResults(results, config)
}

func (monitor *BackgroundMonitor) run() {
	defer func() {
		monitor.cancelActiveCycle()
		monitor.workers.Wait()
		monitor.stateStore.ClearActiveCycle()
		close(monitor.doneCh)
	}()
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

		monitor.startTargetWorkers()
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

func (monitor *BackgroundMonitor) startTargetWorkers() {
	monitor.cycleMu.Lock()
	defer monitor.cycleMu.Unlock()
	if monitor.IsPaused() {
		return
	}
	select {
	case <-monitor.stopCh:
		return
	default:
	}
	if monitor.cancelCycle != nil {
		monitor.cancelCycle()
	}
	monitor.stateStore.ClearActiveCycle()
	ctx, cancel := context.WithCancel(context.Background())
	monitor.cancelCycle = cancel
	config := monitor.configManager.Snapshot()
	generation := monitor.generation.Load()

	monitor.stateStore.SyncTargets(config)
	targets := config.EnabledTargets()
	if len(targets) == 0 {
		cycleID := int(monitor.cycleID.Add(1))
		monitor.stateStore.HandleCycle(nil, config, cycleID)
		return
	}

	for index, target := range targets {
		monitor.workers.Add(1)
		go func(index int, target TargetSpec) {
			defer monitor.workers.Done()
			monitor.runTargetWorker(ctx, config, target, generation, index+1, fmt.Sprintf("target-%d", index+1))
		}(index, target)
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

		cycleID := int(monitor.cycleID.Add(1))
		checkWorkerID := workerID
		if checkWorkerID == "" {
			checkWorkerID = fmt.Sprintf("target-%d", index)
		}
		monitor.cycleMu.Lock()
		if ctx.Err() != nil {
			monitor.cycleMu.Unlock()
			return
		}
		monitor.stateStore.BeginTargetCheck(cycleID, generation, monitor.configManager.Snapshot(), target, time.Now())
		monitor.cycleMu.Unlock()
		result := monitor.coordinator.CheckTargetContext(ctx, target, config.PingTimeoutMS, cycleID, checkWorkerID)
		// Acquire logging order before accepting the result. Holding this through
		// the append prevents an older failure from being logged after recovery.
		monitor.logMu.Lock()
		monitor.cycleMu.Lock()
		if ctx.Err() != nil || result.ErrorCategory == "canceled" {
			monitor.cycleMu.Unlock()
			monitor.logMu.Unlock()
			return
		}
		result.Sequence = monitor.sequence.Add(1)
		currentConfig := monitor.configManager.Snapshot()
		monitor.stateStore.HandleTargetResult(result, currentConfig, cycleID, generation)
		monitor.cycleMu.Unlock()
		monitor.logResultsLocked([]CheckResult{result}, currentConfig)
		monitor.logMu.Unlock()

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
	defer monitor.cycleMu.Unlock()
	if monitor.cancelCycle != nil {
		monitor.cancelCycle()
	}
}

// Return only new errors so repeated failures do not flood the event history.
func (monitor *BackgroundMonitor) logResults(results []CheckResult, config AppConfig) error {
	monitor.logMu.Lock()
	defer monitor.logMu.Unlock()
	return monitor.logResultsLocked(results, config)
}

// The caller holds logMu, and must release cycleMu before filesystem I/O.
func (monitor *BackgroundMonitor) logResultsLocked(results []CheckResult, config AppConfig) error {
	err := monitor.logger.LogResults(results, config)
	if err == nil {
		monitor.lastLogError = ""
		return nil
	}
	if err.Error() == monitor.lastLogError {
		return nil
	}
	monitor.lastLogError = err.Error()
	monitor.stateStore.AddEvent("warn", err.Error(), time.Time{})
	return err
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
		results[index].Sequence = monitor.sequence.Add(1)
	}
}
