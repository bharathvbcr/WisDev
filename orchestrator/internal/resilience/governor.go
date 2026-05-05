package resilience

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/telemetry"
	"sync"
	"time"
)

// SystemHealth represents the current resource utilization of the process.
type SystemHealth struct {
	CPUUsage    float64
	MemoryUsage uint64
	Goroutines  int
	Status      string // "healthy", "degraded", "critical"
}

// ResourceGovernor monitors system resources and enforces admission control.
type ResourceGovernor struct {
	mu             sync.RWMutex
	health         SystemHealth
	maxMemory      uint64
	maxRoutines    int
	maxActiveTasks int
	activeTasks    int
	pollInterval   time.Duration
	stopChan       chan struct{}
}

// NewResourceGovernor creates a governor with the specified limits.
func NewResourceGovernor(maxMemMB uint64, maxRoutines int) *ResourceGovernor {
	return NewResourceGovernorWithInterval(maxMemMB, maxRoutines, 5*time.Second)
}

// NewResourceGovernorWithInterval creates a governor with a custom poll interval.
func NewResourceGovernorWithInterval(maxMemMB uint64, maxRoutines int, interval time.Duration) *ResourceGovernor {
	g := &ResourceGovernor{
		maxMemory:      maxMemMB * 1024 * 1024,
		maxRoutines:    maxRoutines,
		maxActiveTasks: maxRoutines,
		pollInterval:   interval,
		stopChan:       make(chan struct{}),
	}
	if interval > 0 {
		go g.monitor()
	}
	return g
}

// Stop stops the background monitor.
func (g *ResourceGovernor) Stop() {
	close(g.stopChan)
}

// SetStatus manually sets the status (useful for tests).
func (g *ResourceGovernor) SetStatus(status string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.health.Status = status
}

func (g *ResourceGovernor) SetActiveTasksForTest(active int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.activeTasks = active
}

func (g *ResourceGovernor) ActiveTasksForTest() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.activeTasks
}

// Admit checks if a new heavy task should be allowed to start.
func (g *ResourceGovernor) Admit(ctx context.Context, taskType string) error {
	release, err := g.TryAcquire(ctx, taskType)
	if err != nil {
		return err
	}
	release()
	return nil
}

// TryAcquire admits a task and returns a release function that must be called
// when the admitted work finishes.
func (g *ResourceGovernor) TryAcquire(ctx context.Context, taskType string) (func(), error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if err := g.admitLocked(taskType); err != nil {
		return nil, err
	}
	g.activeTasks++

	var once sync.Once
	release := func() {
		once.Do(func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			if g.activeTasks > 0 {
				g.activeTasks--
			}
		})
	}
	return release, nil
}

func (g *ResourceGovernor) admitLocked(taskType string) error {
	if g.health.Status == "critical" {
		telemetry.RecordResourceRejection(taskType, g.health.Status)
		slog.Warn("resource_governor_reject",
			"service", "go_orchestrator",
			"runtime", "go",
			"component", "resilience",
			"operation", "resource_admission",
			"stage", "admit",
			"task_type", taskType,
			"status", g.health.Status,
			"result", "rejected",
			"error_code", "system_overload",
			"active_tasks", g.activeTasks,
		)
		return fmt.Errorf("system resources exhausted (%s), gracefully rejecting %s load kind=system_overload", g.health.Status, taskType)
	}
	if g.maxActiveTasks > 0 && g.activeTasks >= g.maxActiveTasks {
		telemetry.RecordResourceRejection(taskType, g.health.Status)
		slog.Warn("resource_governor_reject",
			"service", "go_orchestrator",
			"runtime", "go",
			"component", "resilience",
			"operation", "resource_admission",
			"stage", "active_task_limit",
			"task_type", taskType,
			"status", g.health.Status,
			"result", "rejected",
			"error_code", "system_overload",
			"active_tasks", g.activeTasks,
			"max_active_tasks", g.maxActiveTasks,
		)
		return fmt.Errorf("gracefully rejecting %s load: active task limit reached (%d/%d) kind=system_overload", taskType, g.activeTasks, g.maxActiveTasks)
	}

	return nil
}

func (g *ResourceGovernor) GetHealth() SystemHealth {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.health
}

func (g *ResourceGovernor) monitor() {
	ticker := time.NewTicker(g.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			g.UpdateHealth()
		case <-g.stopChan:
			return
		}
	}
}

func (g *ResourceGovernor) UpdateHealth() {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	routines := runtime.NumGoroutine()

	g.mu.Lock()
	defer g.mu.Unlock()

	g.health.MemoryUsage = m.Alloc
	g.health.Goroutines = routines

	if m.Alloc > g.maxMemory || routines > g.maxRoutines {
		g.health.Status = "critical"
		slog.Error("System resources critical", "mem", m.Alloc, "routines", routines)
	} else if m.Alloc > g.maxMemory/2 || routines > g.maxRoutines/2 {
		g.health.Status = "degraded"
		slog.Warn("System resources degraded", "mem", m.Alloc, "routines", routines)
	} else {
		g.health.Status = "healthy"
	}
}
