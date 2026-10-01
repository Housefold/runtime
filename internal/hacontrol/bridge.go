package hacontrol

import (
	"context"
	"regexp"
	"time"

	"github.com/housefold/runtime/internal/bridge"
	"github.com/housefold/runtime/internal/discovery"
)

// No production override exists: enabling ordered state requires a future pinned
// Python barrier/sequence compatibility evidence and reviewed source change.
const orderedBridgeEnabled = false
const orderedBridgeReason = "Version-pinned Python snapshot barrier and sequence compatibility evidence is unavailable."

type BridgeInfo struct {
	Status                     string
	BridgeVersion, CoreVersion string
	Discovery                  bool
	OrderedAdvertised          bool
	OrderedEnabled             bool
	StateSource                string
	Limitation                 string
	Guidance                   string
}

var versionText = regexp.MustCompile(`^[a-zA-Z0-9._+-]{1,64}$`)

func safeBridgeVersion(s string) string {
	if versionText.MatchString(s) {
		return s
	}
	return "unknown"
}
func (n *Native) BridgeSnapshot() BridgeInfo {
	result := n.bridgeClient.Snapshot()
	_, discoveryOK := result.Capabilities["discovery"]
	_, orderedOK := result.Capabilities["ordered_state"]
	n.mu.Lock()
	active, fault := n.bridgeSelected && n.discoveryFresh, n.bridgeError
	n.mu.Unlock()
	status := string(result.Status)
	if fault {
		status = string(bridge.Unavailable)
	} else if result.Status == bridge.Available && active {
		status = "active"
	}
	return BridgeInfo{Status: status, BridgeVersion: safeBridgeVersion(result.BridgeVersion), CoreVersion: safeBridgeVersion(result.CoreVersion), Discovery: discoveryOK, OrderedAdvertised: orderedOK, OrderedEnabled: orderedBridgeEnabled, StateSource: "native", Limitation: orderedBridgeReason, Guidance: "Bridge is optional. When a supported Housefold Bridge release is available, follow its Home Assistant integration installation guide. Runtime detects compatible installation automatically; native HA remains available."}
}
func (n *Native) probeBridge(ctx context.Context, now time.Time) {
	result, err := n.bridgeClient.Probe(ctx, now)
	n.mu.Lock()
	n.bridgeError = err != nil && result.Status == bridge.Available
	n.mu.Unlock()
	if err != nil {
		return
	}
	if _, ok := result.Capabilities["discovery"]; !ok {
		return
	}
	if _, err = n.bridgeClient.FetchDiscovery(ctx); err != nil {
		n.mu.Lock()
		n.bridgeSelected = false
		n.bridgeError = true
		n.mu.Unlock()
	}
}

// enrichDiscovery requires caller's native publication lock. It replaces only
// available enrichment sections, retaining native generic facts and explicit
// permission/absence distinctions on each provider. State values are never read.
func (n *Native) enrichDiscovery(native discovery.Snapshot) (discovery.Snapshot, bool, error) {
	extra, fresh := n.bridgeClient.LastDiscovery()
	if !fresh {
		return native, false, nil
	}
	out, copyErr := discovery.Normalize(native)
	if copyErr != nil {
		return native, false, copyErr
	}
	used := false
	if extra.EntityRegistryStatus == discovery.Available {
		byID := map[string]int{}
		for i, e := range out.Entities {
			byID[e.EntityID] = i
		}
		for _, e := range extra.Entities {
			if i, ok := byID[e.EntityID]; ok {
				old := out.Entities[i]
				if !old.Identity.Weak && !e.Identity.Weak && old.Identity != e.Identity {
					return native, false, bridge.ErrProtocol
				}
				if !old.Identity.Weak && e.Identity.Weak {
					e.Identity = old.Identity
				}
				if e.Status == discovery.Available {
					fields := map[string]bool{}
					for _, f := range e.Attributes {
						fields[f.Name] = true
					}
					for _, f := range old.Attributes {
						if !fields[f.Name] {
							e.Attributes = append(e.Attributes, f)
						}
					}
				}
				out.Entities[i] = e
			} else {
				byID[e.EntityID] = len(out.Entities)
				out.Entities = append(out.Entities, e)
			}
		}
		out.RegistryStatus = extra.RegistryStatus
		out.EntityRegistryStatus = extra.EntityRegistryStatus
		used = true
	}
	if extra.DeviceRegistryStatus == discovery.Available {
		out.Devices = extra.Devices
		out.DeviceRegistryStatus = extra.DeviceRegistryStatus
		used = true
	}
	if extra.AreaRegistryStatus == discovery.Available {
		out.Areas = extra.Areas
		out.AreaRegistryStatus = extra.AreaRegistryStatus
		used = true
	}
	if extra.ServicesStatus == discovery.Available {
		out.Services = extra.Services
		out.ServicesStatus = extra.ServicesStatus
		used = true
	}
	normalized, err := discovery.Normalize(out)
	if err != nil {
		return native, false, err
	}
	return normalized, used, nil
}
