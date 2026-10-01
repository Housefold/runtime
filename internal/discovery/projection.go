package discovery

import (
	"context"
	"github.com/housefold/runtime/internal/module"
)

// Send projects only the canonical model. Oversized transport frames fail
// explicitly; consumers never receive registry transport/page envelopes.
func Send(ctx context.Context, s *module.Session, snapshot Snapshot) error {
	normalized, err := Normalize(snapshot)
	if err != nil {
		return err
	}
	frame, err := module.FrameOf("discovery_reset", normalized)
	if err != nil {
		return err
	}
	return s.Send(ctx, frame)
}
