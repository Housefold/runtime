package estate

import (
	"context"
	"time"

	"github.com/housefold/runtime/internal/catalog"
	"github.com/housefold/runtime/internal/packageverify"
)

func (e *Engine) CatalogStatus(now time.Time) catalog.Snapshot {
	if e.config.Catalog == nil {
		return catalog.Snapshot{Status: "unconfigured"}
	}
	return e.config.Catalog.Snapshot(now)
}
func (e *Engine) ReviewInstall(id, version string, now time.Time) (string, []packageverify.Manifest, error) {
	if e.config.Catalog == nil {
		return "", nil, catalog.ErrUnavailable
	}
	return e.config.Catalog.Review(id, version, now)
}
func (e *Engine) Install(ctx context.Context, id, version, reviewedDigest string, now time.Time) error {
	if !e.available() {
		return ErrRecovery
	}
	if e.config.Catalog == nil {
		return catalog.ErrUnavailable
	}
	packages, err := e.config.Catalog.Prepare(ctx, id, version, reviewedDigest, now)
	if err != nil {
		return err
	}
	bundles := []Bundle{}
	artifacts := [][]byte{}
	e.op.Lock()
	if !e.available() {
		e.op.Unlock()
		return ErrRecovery
	}
	for _, p := range packages {
		var identity string
		review, err := packageverify.Inspect(e.config.Authority, p.Catalog, p.Manifest, now)
		if err != nil {
			e.op.Unlock()
			return err
		}
		identity = review.Identity
		row := e.inv.Modules[identity]
		if row.Installed && row.Desired && row.Current != nil && packageverify.Digest(row.Current.Manifest.Data) == packageverify.Digest(p.Manifest.Data) && e.router.Accepting(identity) {
			continue
		}
		bundles = append(bundles, Bundle{Catalog: p.Catalog, Manifest: p.Manifest, ArtifactSignature: p.Signature})
		artifacts = append(artifacts, p.Artifact)
	}
	e.op.Unlock()
	if len(bundles) == 0 {
		return nil
	}
	// Stage revalidates every byte and current desired dependencies after download.
	return e.Stage(ctx, bundles, artifacts, now)
}
