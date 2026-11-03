package downloader

import (
	"math"
	"time"
)

type streamAccelerationPolicy struct {
	sample, settle                time.Duration
	history, addition, maxWorkers int
	stability, requiredGain       float64
	minRemaining                  int64
}

var defaultStreamAccelerationPolicy = streamAccelerationPolicy{
	sample: time.Second, settle: 5 * time.Second, history: 5,
	addition: 4, maxWorkers: 16, stability: 0.15, requiredGain: 0.8, minRemaining: 512 * 1024,
}

// Ghost's auto speed-up principle: wait for a stable throughput plateau, add
// four workers, then keep increasing only if measured speed justifies it.
// Worker settings seed concurrency; the global response budget remains hard.
type streamAccelerator struct {
	policy          streamAccelerationPolicy
	rates           []float64
	baseline        float64
	baselineWorkers int
	checkAt         time.Time
	spent           bool
}

func (a *streamAccelerator) additional(now time.Time, rate float64, workers int, remaining int64) int {
	if a.spent || rate <= 0 || workers <= 0 || remaining < a.policy.minRemaining {
		return 0
	}
	a.rates = append(a.rates, rate)
	if len(a.rates) > a.policy.history {
		a.rates = a.rates[1:]
	}
	if len(a.rates) < a.policy.history {
		return 0
	}
	var average float64
	for _, r := range a.rates {
		average += r
	}
	average /= float64(len(a.rates))
	if !a.checkAt.IsZero() {
		if now.Sub(a.checkAt) < a.policy.settle {
			return 0
		}
		workerGain := float64(workers-a.baselineWorkers) / float64(a.baselineWorkers)
		if workerGain <= 0 || (average-a.baseline)/a.baseline < a.policy.requiredGain*workerGain {
			a.spent = true
			return 0
		}
		a.checkAt = time.Time{}
		a.rates = nil
		return 0
	}
	for _, r := range a.rates {
		if math.Abs(r-average)/average > a.policy.stability {
			return 0
		}
	}
	count := min(a.policy.addition, a.policy.maxWorkers-workers)
	count = min(count, int(remaining/a.policy.minRemaining)-1)
	if count <= 0 {
		return 0
	}
	a.baseline, a.baselineWorkers, a.checkAt = average, workers, now
	return count
}
