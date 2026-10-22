package monitor

import (
	"context"
	"sync"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/config"
	"github.com/Erfan-Abouei/Abouei_Checker/internal/probe"
)

// CheckedBackend pairs a probe result with its updated threshold snapshot.
type CheckedBackend struct {
	// Backend is the configured backend that was probed.
	Backend config.Backend
	// Result is the normalized probe result.
	Result probe.Result
	// State is the streak snapshot after recording the result.
	State Snapshot
}

// Prober is the narrow probe dependency consumed by the monitor package. The
// concrete *probe.Prober implements it.
type Prober interface {
	Check(ctx context.Context, backend config.Backend) probe.Result
}

// RunOnce executes one check for every backend concurrently, bounded by
// maxConcurrency, and returns outcomes in configuration order. One backend's
// timeout or failure never prevents other backends from completing.
// Canceled results are recorded without mutating streaks.
//
// maxConcurrency values <= 0 default to 1.
func RunOnce(
	ctx context.Context,
	backends []config.Backend,
	prober Prober,
	tracker *Tracker,
	maxConcurrency int,
) []CheckedBackend {
	if maxConcurrency <= 0 {
		maxConcurrency = 1
	}
	outcomes := make([]CheckedBackend, len(backends))
	if len(backends) == 0 {
		return outcomes
	}

	semaphore := make(chan struct{}, maxConcurrency)
	var wg sync.WaitGroup
	for i, backend := range backends {
		wg.Add(1)
		go func(index int, current config.Backend) {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				result := probe.Result{
					BackendID:     current.ID,
					Name:          current.Name,
					Outcome:       probe.OutcomeCanceled,
					FailureReason: probe.ReasonCanceled,
					Error:         "probe canceled",
					ObservedAt:    nowUTC(),
				}
				state := Snapshot{
					BackendID:        current.ID,
					Name:             current.Name,
					FailureThreshold: current.FailureThreshold,
				}
				if tracker != nil {
					state = tracker.Record(result)
				}
				outcomes[index] = CheckedBackend{Backend: current, Result: result, State: state}
				return
			}

			result := prober.Check(ctx, current)
			state := Snapshot{
				BackendID:        current.ID,
				Name:             current.Name,
				FailureThreshold: current.FailureThreshold,
			}
			if tracker != nil {
				state = tracker.Record(result)
			}
			outcomes[index] = CheckedBackend{Backend: current, Result: result, State: state}
		}(i, backend)
	}
	wg.Wait()
	return outcomes
}
