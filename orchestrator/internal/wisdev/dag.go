package wisdev

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// TaskStatus represents the current state of a task in the DAG.
type TaskStatus string

const (
	TaskPending   TaskStatus = "pending"
	TaskRunning   TaskStatus = "running"
	TaskCompleted TaskStatus = "completed"
	TaskFailed    TaskStatus = "failed"
	TaskSkipped   TaskStatus = "skipped"
)

// TaskContext provides an isolated context for a specific task execution.
// It contains only the relevant inputs and results from upstream dependencies.
type TaskContext struct {
	Step             PlanStep
	UpstreamResults  map[string]TaskResult
	Scratchpad       map[string]string
	ThoughtSignature string
}

// TaskResult captures the outcome of a single task execution.
type TaskResult struct {
	ID         string
	Status     TaskStatus
	Error      error
	Result     map[string]any
	Sources    []Source
	Confidence float64
	Duration   time.Duration
}

const (
	MaxDAGDepth = 10
	MaxDAGWidth = 20
)

// ResearchDAG implements a concurrent execution engine for research plans.
type ResearchDAG struct {
	session  *AgentSession
	steps    map[string]PlanStep
	status   map[string]TaskStatus
	results  map[string]TaskResult
	mu       sync.RWMutex
	ready    chan string
	done     chan TaskResult
	events   chan<- PlanExecutionEvent
	executor func(ctx context.Context, tctx TaskContext) TaskResult

	// Defensive limits
	maxDepth int
	maxWidth int
}

// NewResearchDAG initializes a new DAG engine for a given session.
func NewResearchDAG(
	session *AgentSession,
	events chan<- PlanExecutionEvent,
	executor func(ctx context.Context, tctx TaskContext) TaskResult,
) *ResearchDAG {
	dag := &ResearchDAG{
		session:  session,
		steps:    make(map[string]PlanStep),
		status:   make(map[string]TaskStatus),
		results:  make(map[string]TaskResult),
		ready:    make(chan string, len(session.Plan.Steps)),
		done:     make(chan TaskResult, len(session.Plan.Steps)),
		events:   events,
		executor: executor,
		maxDepth: MaxDAGDepth,
		maxWidth: MaxDAGWidth,
	}

	if len(session.Plan.Steps) > dag.maxWidth {
		slog.Warn("DAG width exceeds limit, capping", "width", len(session.Plan.Steps))
	}

	for _, step := range session.Plan.Steps {
		dag.steps[step.ID] = step
		dag.status[step.ID] = TaskPending
	}

	return dag
}

// Validate ensures the DAG is not malformed or too complex.
func (d *ResearchDAG) Validate() error {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if len(d.steps) > d.maxWidth {
		return fmt.Errorf("DAG width %d exceeds safety limit of %d", len(d.steps), d.maxWidth)
	}

	// Simple cycle and depth check
	for id := range d.steps {
		depth := d.checkDepth(id, make(map[string]bool))
		if depth > d.maxDepth {
			return fmt.Errorf("DAG depth for step %s exceeds safety limit of %d", id, d.maxDepth)
		}
	}

	return nil
}

func (d *ResearchDAG) checkDepth(id string, path map[string]bool) int {
	if path[id] {
		return 1000 // Cycle detected
	}
	path[id] = true
	defer delete(path, id)

	step, ok := d.steps[id]
	if !ok || len(step.DependsOnStepIDs) == 0 {
		return 1
	}

	maxDepDepth := 0
	for _, depID := range step.DependsOnStepIDs {
		depDepth := d.checkDepth(depID, path)
		if depDepth > maxDepDepth {
			maxDepDepth = depDepth
		}
	}

	return 1 + maxDepDepth
}

// Execute runs the DAG to completion or until a terminal error/cancellation.
func (d *ResearchDAG) Execute(ctx context.Context) error {
	if err := d.Validate(); err != nil {
		return fmt.Errorf("DAG validation failed: %w", err)
	}

	var wg sync.WaitGroup
	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Initial trigger: enqueue steps with no dependencies
	d.triggerReadySteps()

	var loopErr error
Loop:
	for {
		if d.allTerminal() {
			break Loop
		}

		// Deadlock detection: no steps ready, no steps running, but not all terminal
		d.mu.RLock()
		running := 0
		for _, s := range d.status {
			if s == TaskRunning {
				running++
			}
		}
		readyLen := len(d.ready)
		d.mu.RUnlock()

		if running == 0 && readyLen == 0 {
			loopErr = fmt.Errorf("DAG deadlock: no ready steps and none running")
			break Loop
		}

		select {
		case stepID := <-d.ready:
			d.mu.Lock()
			if d.status[stepID] != TaskPending {
				d.mu.Unlock()
				continue
			}
			d.status[stepID] = TaskRunning
			step := d.steps[stepID]

			// Build isolated TaskContext
			upstream := make(map[string]TaskResult)
			for _, depID := range step.DependsOnStepIDs {
				if res, ok := d.results[depID]; ok {
					upstream[depID] = res
				}
			}

			tctx := TaskContext{
				Step:             step,
				UpstreamResults:  upstream,
				Scratchpad:       d.session.ResearchScratchpad,
				ThoughtSignature: d.session.ThoughtSignature,
			}
			d.mu.Unlock()

			wg.Add(1)
			go func(tc TaskContext) {
				defer wg.Done()
				res := d.executor(subCtx, tc)
				select {
				case d.done <- res:
				case <-subCtx.Done():
				}
			}(tctx)

		case res := <-d.done:
			d.mu.Lock()
			d.results[res.ID] = res
			if res.Error != nil {
				d.status[res.ID] = TaskFailed
				slog.Error("Task failed in DAG", "stepId", res.ID, "error", res.Error)
			} else {
				d.status[res.ID] = TaskCompleted
				slog.Info("Task completed in DAG", "stepId", res.ID)
			}
			d.mu.Unlock()

			d.triggerReadySteps()

		case <-ctx.Done():
			loopErr = ctx.Err()
			break Loop
		}
	}

	cancel()  // Cancel sub-context to signal running tasks
	wg.Wait() // Wait for all task goroutines to exit
	return loopErr
}

func (d *ResearchDAG) triggerReadySteps() {
	d.mu.Lock()
	defer d.mu.Unlock()

	for id, step := range d.steps {
		if d.status[id] != TaskPending {
			continue
		}

		ready := true
		for _, depID := range step.DependsOnStepIDs {
			if d.status[depID] != TaskCompleted {
				ready = false
				break
			}
		}

		if ready {
			d.ready <- id
		}
	}
}

func (d *ResearchDAG) allTerminal() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()

	for _, status := range d.status {
		if status == TaskPending || status == TaskRunning {
			return false
		}
	}
	return true
}

func (d *ResearchDAG) GetResults() map[string]TaskResult {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.results
}
