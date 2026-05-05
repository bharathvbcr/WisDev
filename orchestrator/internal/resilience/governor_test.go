package resilience

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestResourceGovernor(t *testing.T) {
	is := assert.New(t)

	t.Run("admit healthy system", func(t *testing.T) {
		g := NewResourceGovernorWithInterval(100, 100, 0)
		g.SetStatus("healthy")
		err := g.Admit(context.Background(), "test")
		is.NoError(err)
	})

	t.Run("reject critical system", func(t *testing.T) {
		g := NewResourceGovernorWithInterval(100, 100, 0)
		g.SetStatus("critical")
		err := g.Admit(context.Background(), "test")
		is.Error(err)
		is.Contains(err.Error(), "system resources exhausted")
		is.Contains(err.Error(), "kind=system_overload")
	})

	t.Run("rejects explicit concurrent task pressure before memory is critical", func(t *testing.T) {
		g := NewResourceGovernorWithInterval(100, 100, 0)
		g.SetStatus("healthy")
		g.SetActiveTasksForTest(100)
		err := g.Admit(context.Background(), "wisdev_research_loop")
		is.Error(err)
		is.Contains(err.Error(), "gracefully rejecting wisdev_research_loop")
		is.Contains(err.Error(), "kind=system_overload")
	})

	t.Run("try acquire tracks active work until released", func(t *testing.T) {
		g := NewResourceGovernorWithInterval(100, 1, 0)
		g.SetStatus("healthy")

		release, err := g.TryAcquire(context.Background(), "wisdev_research_loop")
		is.NoError(err)
		is.Equal(1, g.ActiveTasksForTest())

		_, err = g.TryAcquire(context.Background(), "wisdev_research_loop")
		is.Error(err)
		is.Contains(err.Error(), "kind=system_overload")

		release()
		release()
		is.Equal(0, g.ActiveTasksForTest())
	})

	t.Run("concurrent acquire rejects excess work and releases all slots", func(t *testing.T) {
		g := NewResourceGovernorWithInterval(100, 2, 0)
		g.SetStatus("healthy")

		hold := make(chan struct{})
		var admitted int32
		var rejected int32
		var wg sync.WaitGroup
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				release, err := g.TryAcquire(context.Background(), "wisdev_research_loop")
				if err != nil {
					atomic.AddInt32(&rejected, 1)
					return
				}
				atomic.AddInt32(&admitted, 1)
				<-hold
				release()
			}()
		}

		requireEventually(t, func() bool {
			return atomic.LoadInt32(&admitted)+atomic.LoadInt32(&rejected) == 8
		})
		close(hold)
		wg.Wait()
		is.Equal(int32(2), atomic.LoadInt32(&admitted))
		is.Equal(int32(6), atomic.LoadInt32(&rejected))
		is.Equal(0, g.ActiveTasksForTest())
	})

	t.Run("get health", func(t *testing.T) {
		g := NewResourceGovernorWithInterval(100, 100, 0)
		g.SetStatus("degraded")
		h := g.GetHealth()
		is.Equal("degraded", h.Status)
	})

	t.Run("update health logic", func(t *testing.T) {
		// Set very low limits to trigger thresholds
		g := NewResourceGovernorWithInterval(1, 1, 0) // 1MB, 1 routine
		g.UpdateHealth()
		h := g.GetHealth()
		// Current process will definitely exceed 1MB and 1 routine
		is.Equal("critical", h.Status)
	})

	t.Run("healthy and degraded thresholds", func(t *testing.T) {
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		routines := runtime.NumGoroutine()

		healthy := NewResourceGovernorWithInterval(1024, routines+100, 0)
		healthy.UpdateHealth()
		is.Equal("healthy", healthy.GetHealth().Status)

		degraded := NewResourceGovernorWithInterval(1024, routines+1, 0)
		degraded.UpdateHealth()
		is.Equal("degraded", degraded.GetHealth().Status)
	})

	t.Run("constructor and stop", func(t *testing.T) {
		g := NewResourceGovernor(128, 128)
		is.Equal(5*time.Second, g.pollInterval)
		is.Equal(uint64(128*1024*1024), g.maxMemory)
		g.Stop()
	})

	t.Run("monitor loop ticks and stops", func(t *testing.T) {
		g := NewResourceGovernorWithInterval(1024, 1024, time.Millisecond)
		defer g.Stop()

		time.Sleep(15 * time.Millisecond)
		h := g.GetHealth()
		is.NotEmpty(h.Status)
	})
}

func requireEventually(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition did not become true")
}
