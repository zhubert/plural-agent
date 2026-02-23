package daemon

import (
	"context"
	"time"

	"github.com/zhubert/plural-agent/internal/worker"
)

// waitForActiveWorkers waits for all active workers to complete (used in --once mode).
func (d *Daemon) waitForActiveWorkers(ctx context.Context) {
	d.mu.Lock()
	workers := make([]*worker.SessionWorker, 0, len(d.workers))
	for _, w := range d.workers {
		workers = append(workers, w)
	}
	d.mu.Unlock()

	for _, w := range workers {
		w.Wait()
	}
}

// shutdown gracefully stops all workers and releases the lock.
func (d *Daemon) shutdown() {
	d.mu.Lock()
	workers := make([]*worker.SessionWorker, 0, len(d.workers))
	for _, w := range d.workers {
		workers = append(workers, w)
	}
	d.mu.Unlock()

	d.logger.Info("shutting down workers", "count", len(workers))
	for _, w := range workers {
		w.Cancel()
	}

	done := make(chan struct{})
	go func() {
		for _, w := range workers {
			w.Wait()
		}
		close(done)
	}()

	select {
	case <-done:
		d.logger.Info("all workers shut down")
	case <-time.After(30 * time.Second):
		d.logger.Warn("shutdown timed out")
	}

	d.saveState()
	d.sessionMgr.Shutdown()
}

// releaseLock releases the daemon lock.
func (d *Daemon) releaseLock() {
	if d.lock != nil {
		if err := d.lock.Release(); err != nil {
			d.logger.Warn("failed to release lock", "error", err)
		}
	}
}

// saveConfig saves the config with failure tracking.
// The context parameter describes where the save was triggered from for logging.
// After 5 consecutive failures, new work is paused until a save succeeds.
func (d *Daemon) saveConfig(where string) {
	if err := d.config.Save(); err != nil {
		d.configSaveFailures++
		if d.configSaveFailures >= 5 {
			d.logger.Error("config save failed repeatedly, pausing new work to prevent state drift",
				"where", where,
				"consecutiveFailures", d.configSaveFailures,
				"error", err,
			)
			d.configSavePaused = true
		} else {
			d.logger.Warn("config save failed",
				"where", where,
				"consecutiveFailures", d.configSaveFailures,
				"error", err,
			)
		}
	} else {
		if d.configSavePaused {
			d.logger.Info("config save recovered, resuming new work",
				"where", where,
			)
			d.configSavePaused = false
		}
		d.configSaveFailures = 0
	}
}

// saveState persists the daemon state to disk.
func (d *Daemon) saveState() {
	if d.state == nil {
		return
	}
	d.state.LastPollAt = time.Now()
	if err := d.state.Save(); err != nil {
		d.logger.Error("failed to save daemon state", "error", err)
	}
}
