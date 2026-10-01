package estate

import (
	"time"

	"github.com/housefold/runtime/internal/discovery"
	"github.com/housefold/runtime/internal/hacontrol"
)

func (e *Engine) PublishDiscovery(snapshot discovery.Snapshot) error {
	if !e.available() {
		return ErrRecovery
	}
	source, err := e.bindings.Generate(snapshot)
	if err != nil {
		e.mu.Lock()
		e.bindingFresh = false
		e.mu.Unlock()
		return err
	}
	e.mu.Lock()
	e.bindingSource = append([]byte(nil), source...)
	e.bindingFresh = true
	e.mu.Unlock()
	return nil
}
func (e *Engine) Bindings() ([]byte, bool) {
	e.mu.Lock()
	source, fresh := append([]byte(nil), e.bindingSource...), e.bindingFresh
	e.mu.Unlock()
	if e.config.Discovery != nil {
		_, providerFresh := e.config.Discovery.DiscoverySnapshot()
		fresh = fresh && providerFresh
	}
	return source, fresh
}
func (e *Engine) OperationalSnapshot() hacontrol.OperationalSnapshot {
	snapshot := e.Snapshot()
	out := hacontrol.OperationalSnapshot{Runtime: snapshot.Phase, StorageDegraded: snapshot.StorageDegraded, Catalog: e.CatalogStatus(time.Now().UTC()).Status}
	for _, m := range snapshot.Modules {
		out.Modules = append(out.Modules, hacontrol.ModuleSignal{Identity: m.Identity, Version: m.Version, Boot: e.boot, Generation: m.Generation, Phase: m.Phase, Desired: m.Desired, ServiceHealthy: m.ServiceHealthy, UIHealthy: m.UIHealthy, Installed: m.Installed})
	}
	return out
}
