package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/housefold/runtime/internal/ha"
	"github.com/housefold/runtime/internal/state"
	"io"
	"os"
	"testing"
	"time"
)

type orderedPeer struct {
	begin  OrderedFrame
	frames []OrderedFrame
	closed bool
}

func (p *orderedPeer) Exchange(ctx context.Context, raw []byte, limit int) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	body, _ := json.Marshal(p.begin)
	return json.Marshal(Response{ID: 1, Type: "result", Success: true, Result: body})
}
func (p *orderedPeer) Next(ctx context.Context, limit int) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if len(p.frames) == 0 {
		return nil, io.EOF
	}
	f := p.frames[0]
	p.frames = p.frames[1:]
	return json.Marshal(struct {
		Type  string       `json:"type"`
		ID    int          `json:"id"`
		Event OrderedFrame `json:"event"`
	}{"event", 1, f})
}
func (p *orderedPeer) Close() error { p.closed = true; return nil }
func orderedCap() Capability {
	return Capability{Name: "ordered_state", Version: Version{Major: 1}, Required: []string{"snapshot_barrier", "contiguous_sequence"}, Limits: Limits{Frame: MaxFrame, Chunks: MaxChunks, Total: MaxTotal}}
}
func entity(v string) state.Entity {
	return state.Entity{EntityID: "sensor.synthetic", State: v, Attributes: map[string]any{"number": json.Number("9007199254740993")}, LastChanged: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), LastUpdated: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}
func prepareNative(t *testing.T, s *ha.Sources) {
	w, _ := s.Begin(ha.NativeSource, "native", 0)
	w.Stage([]state.Entity{entity("native")})
	w.Complete("native", 0)
	if err := w.Commit(); err != nil {
		t.Fatal(err)
	}
}
func peerFixture() *orderedPeer {
	return &orderedPeer{begin: OrderedFrame{Kind: "snapshot_begin", Epoch: "synthetic-epoch", Sequence: 10}, frames: []OrderedFrame{{Kind: "snapshot_chunk", Epoch: "synthetic-epoch", Sequence: 10, Entities: []state.Entity{entity("bridge")}}, {Kind: "snapshot_end", Epoch: "synthetic-epoch", Sequence: 10}}}
}
func TestOrderedPreparationAndAutomaticNativeFallback(t *testing.T) {
	s := ha.NewSources()
	defer s.Close()
	prepareNative(t, s)
	peer := peerFixture()
	updated := entity("live")
	peer.frames = append(peer.frames, OrderedFrame{Kind: "delta", Epoch: "synthetic-epoch", Sequence: 11, Change: &state.Event{Kind: state.Update, EntityID: updated.EntityID, Entity: &updated}})
	stream, err := PrepareOrdered(context.Background(), s, peer, orderedCap())
	if err != nil || s.Selected() != ha.BridgeSource {
		t.Fatal(err)
	}
	if s.View().Snapshot().States[updated.EntityID].Attributes["number"] != json.Number("9007199254740993") {
		t.Fatal("precision")
	}
	calls := 0
	err = stream.Run(context.Background(), func(context.Context) (state.Snapshot, error) {
		calls++
		return state.Snapshot{Generation: 2, Fresh: true, States: map[string]state.Entity{updated.EntityID: entity("native resync")}}, nil
	})
	if !errors.Is(err, io.EOF) || calls != 1 || !peer.closed || s.Selected() != ha.NativeSource || s.View().Snapshot().Generation != 3 {
		t.Fatal(err, calls, s.View().Snapshot())
	}
}
func TestOrderedFaultsCannotReplaceNative(t *testing.T) {
	for _, mutate := range []func(*orderedPeer){func(p *orderedPeer) { p.begin.Kind = "unknown" }, func(p *orderedPeer) { p.frames[1].Sequence++ }, func(p *orderedPeer) { p.frames[0].Epoch = "other" }, func(p *orderedPeer) { p.frames[0].Entities = append(p.frames[0].Entities, p.frames[0].Entities[0]) }, func(p *orderedPeer) { p.frames = p.frames[:1] }, func(p *orderedPeer) {
		e := entity("gap")
		p.frames[1] = OrderedFrame{Kind: "delta", Epoch: "synthetic-epoch", Sequence: 12, Change: &state.Event{Kind: state.Update, EntityID: e.EntityID, Entity: &e}}
	}} {
		s := ha.NewSources()
		prepareNative(t, s)
		peer := peerFixture()
		mutate(peer)
		if _, err := PrepareOrdered(context.Background(), s, peer, orderedCap()); err == nil || !peer.closed || s.Selected() != ha.NativeSource || !s.View().Snapshot().Fresh {
			t.Fatal(err)
		}
		s.Close()
	}
}
func TestOrderedGapFallbackFailureRetainsStale(t *testing.T) {
	s := ha.NewSources()
	defer s.Close()
	peer := peerFixture()
	e := entity("gap")
	peer.frames = append(peer.frames, OrderedFrame{Kind: "delta", Epoch: "synthetic-epoch", Sequence: 12, Change: &state.Event{Kind: state.Update, EntityID: e.EntityID, Entity: &e}})
	stream, err := PrepareOrdered(context.Background(), s, peer, orderedCap())
	if err != nil {
		t.Fatal(err)
	}
	if err = stream.Run(context.Background(), func(context.Context) (state.Snapshot, error) { return state.Snapshot{}, io.ErrUnexpectedEOF }); err == nil || s.View().Snapshot().Fresh || s.View().Snapshot().States[e.EntityID].State != "bridge" {
		t.Fatal(err)
	}
}
func TestOrderedCapabilityGateAndCancellation(t *testing.T) {
	for _, required := range [][]string{{"snapshot_barrier"}, {"contiguous_sequence"}, {"snapshot_barrier", "contiguous_sequence", "unknown"}} {
		cap := orderedCap()
		cap.Required = required
		s := ha.NewSources()
		if _, err := PrepareOrdered(context.Background(), s, peerFixture(), cap); err == nil {
			t.Fatal(required)
		}
		s.Close()
	}
	s := ha.NewSources()
	defer s.Close()
	prepareNative(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := PrepareOrdered(ctx, s, peerFixture(), orderedCap()); err == nil || s.Selected() != ha.NativeSource {
		t.Fatal(err)
	}
}

func TestSharedOrderedWireFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/ordered_stream_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var frames []json.RawMessage
	if err = json.Unmarshal(raw, &frames); err != nil {
		t.Fatal(err)
	}
	s := ha.NewSources()
	defer s.Close()
	peer := &wireOrderedPeer{frames: frames}
	stream, err := PrepareOrdered(context.Background(), s, peer, orderedCap())
	if err != nil {
		t.Fatal(err)
	}
	if err = stream.next(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.View().Snapshot().States["sensor.synthetic"].Attributes["number"] != json.Number("9007199254740993") {
		t.Fatal("fixture number rounded")
	}
}

type wireOrderedPeer struct{ frames []json.RawMessage }

func (p *wireOrderedPeer) Exchange(ctx context.Context, raw []byte, limit int) ([]byte, error) {
	return p.Next(ctx, limit)
}
func (p *wireOrderedPeer) Next(ctx context.Context, limit int) ([]byte, error) {
	if len(p.frames) == 0 {
		return nil, io.EOF
	}
	raw := p.frames[0]
	p.frames = p.frames[1:]
	return raw, nil
}
func (p *wireOrderedPeer) Close() error { return nil }
