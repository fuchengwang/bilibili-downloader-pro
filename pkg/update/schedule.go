package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Schedule separates the startup check from periodic checks. Zero Interval
// disables periodic checks. With both disabled no background task is started.
type Schedule struct {
	CheckOnStartup bool
	Interval       time.Duration
	StartupDelay   time.Duration // Zero: 5 seconds. Applies to checks due immediately.
	StatePath      string        // Empty: system config directory, separated by AppID.
}

type scheduleState struct {
	LastAttempt time.Time `json:"last_attempt"`
	RetryUntil  time.Time `json:"retry_until,omitempty"`
}

type Monitor struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// Stop is nonblocking, so it is safe to call from an event callback.
func (m *Monitor) Stop()                 { m.cancel() }
func (m *Monitor) Done() <-chan struct{} { return m.done }

// Start launches at most one monitor per client. Events run on its goroutine;
// marshal UI operations onto your framework's UI thread. An error event never
// means "no update". Failed periodic checks wait until the next configured
// interval (or longer if required by Retry-After), rather than looping retries.
func (c *Client) Start(ctx context.Context, policy Schedule, onResult func(*CheckResult, error)) (*Monitor, error) {
	if policy.Interval < 0 || policy.StartupDelay < 0 {
		return nil, errors.New("check intervals must not be negative")
	}
	ctx, cancel := context.WithCancel(ctx)
	m := &Monitor{cancel: cancel, done: make(chan struct{})}
	if !policy.CheckOnStartup && policy.Interval == 0 {
		close(m.done)
		return m, nil
	}
	if onResult == nil {
		cancel()
		return nil, errors.New("automatic checks need a result callback")
	}
	if policy.StartupDelay == 0 {
		policy.StartupDelay = 5 * time.Second
	}
	if policy.StatePath == "" {
		directory, err := os.UserConfigDir()
		if err != nil {
			cancel()
			return nil, err
		}
		policy.StatePath = filepath.Join(directory, "go-apps-updates", c.cfg.AppID, "checks.json")
	}
	state, err := loadSchedule(policy.StatePath)
	if err != nil {
		cancel()
		return nil, err
	}
	c.mu.Lock()
	if c.monitorRunning {
		c.mu.Unlock()
		cancel()
		return nil, ErrMonitorRunning
	}
	c.monitorRunning = true
	if state.RetryUntil.After(c.retryUntil) {
		c.retryUntil = state.RetryUntil
	}
	c.mu.Unlock()
	go func() {
		defer cancel()
		defer close(m.done)
		defer func() { c.mu.Lock(); c.monitorRunning = false; c.mu.Unlock() }()
		now := time.Now()
		// A clock change must not defer checks indefinitely.
		if state.LastAttempt.After(now) {
			state.LastAttempt = now
		}
		next := now.Add(policy.StartupDelay)
		if !policy.CheckOnStartup {
			if state.LastAttempt.IsZero() {
				next = now.Add(policy.Interval)
			} else {
				next = state.LastAttempt.Add(policy.Interval)
			}
			if next.Before(now.Add(policy.StartupDelay)) {
				next = now.Add(policy.StartupDelay)
			}
		}
		for {
			c.mu.Lock()
			if c.retryUntil.After(next) {
				next = c.retryUntil
			}
			c.mu.Unlock()
			timer := time.NewTimer(time.Until(next))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			if ctx.Err() != nil {
				return
			}
			state.LastAttempt = time.Now()
			// Persist before networking: restarting after a failed check must not
			// turn a weekly policy into a repeated startup request.
			if err := saveSchedule(policy.StatePath, state); err != nil {
				onResult(nil, err)
				return
			}
			result, checkErr := c.Check(ctx)
			if ctx.Err() != nil {
				return
			}
			c.mu.Lock()
			state.RetryUntil = c.retryUntil
			c.mu.Unlock()
			if err := saveSchedule(policy.StatePath, state); err != nil {
				onResult(result, errors.Join(checkErr, err))
				return
			}
			onResult(result, checkErr)
			if policy.Interval == 0 {
				return
			}
			next = state.LastAttempt.Add(policy.Interval)
			// A slow check never results in a catch-up loop.
			if next.Before(time.Now()) {
				next = time.Now().Add(policy.Interval)
			}
		}
	}()
	return m, nil
}

func loadSchedule(path string) (scheduleState, error) {
	var state scheduleState
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return state, fmt.Errorf("read update schedule: %w", err)
	}
	defer f.Close()
	// Treat a damaged cache as missing. Authorization and application data are
	// unrelated to this disposable scheduling cache.
	if err := json.NewDecoder(f).Decode(&state); err != nil {
		return scheduleState{}, nil
	}
	return state, nil
}

func saveSchedule(path string, state scheduleState) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return fmt.Errorf("save update schedule: %w", err)
	}
	f, err := os.CreateTemp(directory, ".checks-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := json.NewEncoder(f).Encode(state); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
