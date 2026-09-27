package ha

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestMonitorLogsOnlyStatusTransitions(t *testing.T) {
	statuses := []Status{
		StatusUnavailable, StatusUnavailable,
		StatusDenied, StatusDenied,
		StatusConnected, StatusConnected,
		StatusUnavailable,
	}
	probeIndex := 0
	waitCount := 0
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	monitor(context.Background(), func(context.Context) Result {
		status := statuses[probeIndex]
		probeIndex++
		return Result{Status: status, ErrorCategory: "must-not-be-logged-secret"}
	}, func(context.Context, time.Duration) bool {
		waitCount++
		return waitCount < len(statuses)
	}, logger)

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("log record count = %d, want 4: %s", len(lines), output.String())
	}
	for i, status := range []Status{StatusUnavailable, StatusDenied, StatusConnected, StatusUnavailable} {
		if !strings.Contains(lines[i], `"status":"`+string(status)+`"`) {
			t.Errorf("log %d does not contain transition %q: %s", i, status, lines[i])
		}
	}
	if strings.Contains(output.String(), "must-not-be-logged-secret") {
		t.Fatal("monitor logged the probe error category")
	}
}

func TestMonitorObserverReceivesEveryCoarseResultAndTimestamp(t *testing.T) {
	statuses := []Status{StatusUnavailable, StatusUnavailable, StatusConnected}
	var observed []struct {
		status Status
		at     time.Time
	}
	probeIndex := 0
	waitCount := 0
	monitorObserved(context.Background(), func(context.Context) Result {
		status := statuses[probeIndex]
		probeIndex++
		return Result{Status: status, ErrorCategory: "private probe details"}
	}, func(context.Context, time.Duration) bool {
		waitCount++
		return waitCount < len(statuses)
	}, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), func(status Status, at time.Time) {
		observed = append(observed, struct {
			status Status
			at     time.Time
		}{status: status, at: at})
	})
	if len(observed) != len(statuses) {
		t.Fatalf("observer result count = %d, want %d", len(observed), len(statuses))
	}
	for i, result := range observed {
		if result.status != statuses[i] {
			t.Errorf("observer status %d = %q, want %q", i, result.status, statuses[i])
		}
		if result.at.IsZero() || result.at.Location() != time.UTC {
			t.Errorf("observer timestamp %d = %v, want non-zero UTC time", i, result.at)
		}
	}
}

func TestMonitorBackoffCapAndReset(t *testing.T) {
	statuses := []Status{
		StatusUnavailable, StatusUnavailable, StatusUnavailable, StatusUnavailable,
		StatusUnavailable, StatusUnavailable, StatusUnavailable,
		StatusConnected, StatusUnavailable,
	}
	var got []time.Duration
	probeIndex := 0
	monitor(context.Background(), func(context.Context) Result {
		status := statuses[probeIndex]
		probeIndex++
		return Result{Status: status}
	}, func(_ context.Context, delay time.Duration) bool {
		got = append(got, delay)
		return len(got) < len(statuses)
	}, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))

	want := []time.Duration{
		time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
		16 * time.Second, 30 * time.Second, 30 * time.Second,
		30 * time.Second, time.Second,
	}
	if len(got) != len(want) {
		t.Fatalf("delays = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("delay %d = %s, want %s", i, got[i], want[i])
		}
	}
}

func TestMonitorStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	probeCount := 0
	monitor(ctx, func(context.Context) Result {
		probeCount++
		return Result{Status: StatusUnavailable}
	}, func(context.Context, time.Duration) bool {
		cancel()
		return false
	}, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if probeCount != 1 {
		t.Fatalf("probe count after cancellation = %d, want 1", probeCount)
	}

	canceled, stop := context.WithCancel(context.Background())
	stop()
	if waitContext(canceled, time.Hour) {
		t.Fatal("waitContext reported completion after cancellation")
	}
}
