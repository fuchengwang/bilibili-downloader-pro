package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestScheduleDisabledStartupAndPeriodic(t *testing.T) {
	var requests atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); sendResult(w, nil) }))
	defer ts.Close()
	c := testClient(t, ts)
	if requests.Load() != 0 {
		t.Fatal("New performed networking")
	}
	m, err := c.Start(context.Background(), Schedule{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	<-m.Done()
	if requests.Load() != 0 {
		t.Fatal("disabled monitor performed networking")
	}
	for _, policy := range []Schedule{
		{CheckOnStartup: true, StartupDelay: time.Nanosecond},
		{Interval: 20 * time.Millisecond, StartupDelay: time.Nanosecond},
	} {
		policy.StatePath = filepath.Join(t.TempDir(), "state.json")
		events := make(chan struct{}, 5)
		m, err := c.Start(context.Background(), policy, func(result *CheckResult, err error) {
			if err != nil {
				t.Error(err)
			}
			events <- struct{}{}
		})
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-events:
		case <-time.After(2 * time.Second):
			t.Fatal("scheduled check did not occur")
		}
		m.Stop()
		<-m.Done()
		state, err := loadSchedule(policy.StatePath)
		if err != nil || state.LastAttempt.IsZero() {
			t.Fatal("schedule was not persisted")
		}
	}
}

func TestSchedulePersistsWeekAndStopsCleanly(t *testing.T) {
	var requests atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(500) }))
	defer ts.Close()
	c := testClient(t, ts)
	path := filepath.Join(t.TempDir(), "state.json")
	if err := saveSchedule(path, scheduleState{LastAttempt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	policy := Schedule{Interval: 7 * 24 * time.Hour, StatePath: path}
	m, err := c.Start(context.Background(), policy, func(*CheckResult, error) { t.Error("weekly check ran too early") })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Start(context.Background(), policy, func(*CheckResult, error) {}); err != ErrMonitorRunning {
		t.Fatalf("duplicate monitor: %v", err)
	}
	m.Stop()
	<-m.Done()
	if requests.Load() != 0 {
		t.Fatal("restart lost weekly schedule")
	}
	events := make(chan error, 1)
	policy.CheckOnStartup = true
	policy.StartupDelay = time.Nanosecond
	m, err = c.Start(context.Background(), policy, func(_ *CheckResult, err error) { events <- err })
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-events:
		if err == nil {
			t.Fatal("network failure became no update")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no error callback")
	}
	m.Stop()
	<-m.Done()
	if requests.Load() != 1 {
		t.Fatal("failed check immediately retried")
	}
	state, err := loadSchedule(path)
	if err != nil || state.LastAttempt.IsZero() {
		t.Fatal("failed attempt was not persisted")
	}
}
