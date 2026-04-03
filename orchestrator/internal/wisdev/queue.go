package wisdev

import (
	"context"
	"fmt"
	"sync"
)

// TaskQueue manages a pool of workers to process heavy research tasks.
type TaskQueue struct {
	tasks chan func()
	wg    sync.WaitGroup
}

// NewTaskQueue creates a new TaskQueue with the given buffer size and worker count.
func NewTaskQueue(bufferSize, workerCount int) *TaskQueue {
	q := &TaskQueue{
		tasks: make(chan func(), bufferSize),
	}

	for i := 0; i < workerCount; i++ {
		q.wg.Add(1)
		go q.worker()
	}

	return q
}

func (q *TaskQueue) worker() {
	defer q.wg.Done()
	for task := range q.tasks {
		task()
	}
}

// Submit adds a task to the queue. Returns error if queue is full.
func (q *TaskQueue) Submit(task func()) error {
	select {
	case q.tasks <- task:
		return nil
	default:
		return fmt.Errorf("research queue is full, try again later")
	}
}

// Shutdown stops the queue and waits for all tasks to complete.
func (q *TaskQueue) Shutdown() {
	close(q.tasks)
	q.wg.Wait()
}

// AutonomousWorker wraps the AutonomousLoop with a TaskQueue.
type AutonomousWorker struct {
	loop  *AutonomousLoop
	queue *TaskQueue
}

func NewAutonomousWorker(loop *AutonomousLoop) *AutonomousWorker {
	return &AutonomousWorker{
		loop:  loop,
		queue: NewTaskQueue(100, 5), // 100 pending tasks, 5 concurrent loops
	}
}

func (w *AutonomousWorker) RunAsync(ctx context.Context, req LoopRequest, onComplete func(*LoopResult, error)) error {
	return w.queue.Submit(func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				onComplete(nil, fmt.Errorf("autonomous worker panic: %v", recovered))
			}
		}()

		runCtx := ctx
		if runCtx == nil {
			runCtx = context.Background()
		}

		result, err := w.loop.Run(runCtx, req)
		onComplete(result, err)
	})
}
