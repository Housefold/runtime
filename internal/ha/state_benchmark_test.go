package ha

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type cacheScale struct {
	name        string
	count, blob int
}

var cacheScales = []cacheScale{{"small", 100, 32}, {"large", 5000, 512}, {"near_limit", 16000, 3600}}

func benchmarkCandidate(t testing.TB, scale cacheScale) *stateCandidate {
	t.Helper()
	c, _ := newStateCandidate(nil)
	for i := 0; i < scale.count; i++ {
		id := fmt.Sprintf("sensor.%d", i)
		e := EntityState{EntityID: id, State: "off", Attributes: map[string]any{"blob": strings.Repeat("x", scale.blob), "nested": []any{map[string]any{"v": "synthetic"}}}, LastChanged: time.Unix(1, 0).UTC(), LastUpdated: time.Unix(1, 0).UTC()}
		if err := c.set(id, e); err != nil {
			t.Fatal(err)
		}
	}
	return c
}
func BenchmarkPrivateState(b *testing.B) {
	for _, scale := range cacheScales {
		b.Run(scale.name, func(b *testing.B) {
			s := newStateSession("ws://unused", waitContext)
			c := benchmarkCandidate(b, scale)
			s.publish(c)
			b.Run("Snapshot", func(b *testing.B) {
				b.ReportAllocs()

				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					_ = s.Snapshot()
				}
				b.ReportMetric(float64(c.bytes), "payload-B")
			})
			b.Run("Read", func(b *testing.B) {
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					_ = s.Read("sensor.0")
				}
			})
			base := cloneEntityState(s.states["sensor.0"])
			b.Run("Delta", func(b *testing.B) {
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if !s.applyLive(benchmarkDelta(base, i+2)) {
						b.Fatal("delta failed")
					}
				}
			})
			b.Run("UpdateWithReader", func(b *testing.B) {
				// Fresh generation prevents calibration iterations from sharing watermarks.
				s := newStateSession("ws://unused", waitContext)
				s.publish(benchmarkCandidate(b, scale))
				base := cloneEntityState(s.states["sensor.0"])
				ctx, cancel := context.WithCancel(context.Background())
				done := make(chan struct{})
				start := make(chan struct{})
				go func() {
					defer close(done)
					close(start)
					for ctx.Err() == nil {
						_ = s.Snapshot()
					}
				}()
				<-start
				b.ReportAllocs()
				b.ResetTimer()
				var maxDelay time.Duration
				for i := 0; i < b.N; i++ {
					before := time.Now()
					if !s.applyLive(benchmarkDelta(base, i+2)) {
						b.Fatal("delta failed")
					}
					delay := time.Since(before)
					if delay > maxDelay {
						maxDelay = delay
					}
				}
				b.StopTimer()
				cancel()
				<-done
				b.ReportMetric(float64(maxDelay.Nanoseconds()), "max-update-ns")
			})
		})
	}
}

func TestSyntheticMemoryAndNearLimitCopyCancellation(t *testing.T) {
	scale := cacheScales[2]
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	s := newStateSession("ws://unused", waitContext)
	c := benchmarkCandidate(t, scale)
	s.publish(c)
	runtime.GC()
	var baseline runtime.MemStats
	runtime.ReadMemStats(&baseline)
	// Retain published + candidate + independently owned consumer copy to
	// measure actual memory amplification for this synthetic shape.
	next := benchmarkCandidate(t, scale)
	snap := s.Snapshot()
	runtime.GC()
	var coexist runtime.MemStats
	runtime.ReadMemStats(&coexist)
	t.Logf("payload=%d resident-cache-heap=%d retained-cache+candidate+copy-heap=%d amplification=%.2fx", c.bytes, baseline.HeapAlloc-before.HeapAlloc, coexist.HeapAlloc-before.HeapAlloc, float64(coexist.HeapAlloc-before.HeapAlloc)/float64(c.bytes))
	runtime.KeepAlive(next)
	runtime.KeepAlive(snap)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	enteredWait := make(chan struct{})
	s.wait = func(ctx context.Context, _ time.Duration) bool { close(enteredWait); <-ctx.Done(); return false }
	done := make(chan struct{})
	go func() { s.Run(ctx, ""); close(done) }()
	<-enteredWait
	copying := make(chan struct{})
	allowCopy := make(chan struct{})
	copyDone := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		s.mu.RLock()
		close(copying)
		<-allowCopy
		_ = cloneStateMap(s.states)
		s.mu.RUnlock()
		close(copyDone)
	})
	<-copying
	started := time.Now()
	cancel()
	close(allowCopy)
	select {
	case <-done:
	case <-time.After(9 * time.Second):
		t.Fatal("near-limit copy blocked session exit")
	}
	<-copyDone
	wg.Wait()
	t.Logf("session exit while near-limit copy held read gate: %s", time.Since(started))
	if s.Metadata().Fresh {
		t.Fatal("shutdown left state fresh")
	}
}

func benchmarkDelta(base EntityState, tick int) stateEvent {
	base.State = fmt.Sprint(tick)
	base.LastUpdated = time.Unix(int64(tick), 0).UTC()
	raw, _ := json.Marshal(base)
	event := stateEvent{EventType: "state_changed", TimeFired: base.LastUpdated.Format(time.RFC3339Nano)}
	event.Data.EntityID = base.EntityID
	event.Data.NewState = raw
	return event
}
