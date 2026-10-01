package bridge

import (
	"context"
	"encoding/json"
	"github.com/housefold/runtime/internal/discovery"
	"strings"
)

type Section[T any] struct {
	Status discovery.Availability `json:"status"`
	Rows   []T                    `json:"rows"`
}
type RegistryEntity struct {
	RegistryID string                 `json:"registry_id"`
	EntityID   string                 `json:"entity_id"`
	Name       string                 `json:"name"`
	DeviceID   string                 `json:"device_id"`
	AreaID     string                 `json:"area_id"`
	Status     discovery.Availability `json:"status"`
	Attributes []discovery.Field      `json:"attributes"`
}
type RegistryObject struct {
	ID     string                 `json:"id"`
	Name   string                 `json:"name"`
	AreaID string                 `json:"area_id"`
	Status discovery.Availability `json:"status"`
}
type Page struct {
	Schema   int                        `json:"schema"`
	Epoch    string                     `json:"epoch"`
	Index    int                        `json:"index"`
	More     bool                       `json:"more"`
	Cursor   string                     `json:"cursor"`
	Entities Section[RegistryEntity]    `json:"entities"`
	Devices  Section[RegistryObject]    `json:"devices"`
	Areas    Section[RegistryObject]    `json:"areas"`
	Services Section[discovery.Service] `json:"services"`
}

func validSection[T any](s Section[T]) bool {
	return (s.Status == discovery.Available) || (len(s.Rows) == 0 && (s.Status == discovery.Missing || s.Status == discovery.Unsupported || s.Status == discovery.Redacted))
}
func registryIdentity(kind, id string) discovery.Identity {
	return discovery.Identity{Provider: "ha", Key: kind + "/" + id}
}
func (c *Client) LastDiscovery() (discovery.Snapshot, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lastDiscovery == nil {
		return discovery.Snapshot{}, false
	}
	copy, _ := discovery.Normalize(*c.lastDiscovery)
	return copy, c.discoveryFresh
}
func (c *Client) FetchDiscovery(ctx context.Context) (out discovery.Snapshot, err error) {
	select {
	case c.gate <- struct{}{}:
		defer func() { <-c.gate }()
	default:
		return out, ErrBusy
	}
	result := c.Snapshot()
	cap, ok := result.Capabilities["discovery"]
	if result.Status != Available || !ok {
		return out, ErrProtocol
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	defer func() {
		if err != nil {
			c.mu.Lock()
			c.discoveryFresh = false
			c.mu.Unlock()
		}
	}()
	out = discovery.Snapshot{Schema: 1}
	total := 0
	cursor, epoch := "", ""
	seenCursors := map[string]bool{}
	for index := 0; index < cap.Limits.Chunks; index++ {
		response, exchangeErr := c.exchange(ctx, "housefold/discovery", struct {
			Cursor string `json:"cursor"`
			Limit  int    `json:"limit"`
		}{cursor, cap.Limits.Frame}, cap.Limits.Frame)
		if exchangeErr != nil {
			return discovery.Snapshot{}, exchangeErr
		}
		if !response.Success {
			return discovery.Snapshot{}, ErrProtocol
		}
		total += len(response.Result)
		if total > cap.Limits.Total {
			return discovery.Snapshot{}, ErrProtocol
		}
		var page Page
		if json.Unmarshal(response.Result, &page) != nil || page.Schema != 1 || page.Epoch == "" || len(page.Epoch) > 128 || page.Index != index || len(page.Cursor) > 128 || !validSection(page.Entities) || !validSection(page.Devices) || !validSection(page.Areas) || !validSection(page.Services) {
			return discovery.Snapshot{}, ErrProtocol
		}
		if index == 0 {
			epoch = page.Epoch
			out.RegistryStatus = page.Entities.Status
			out.EntityRegistryStatus = page.Entities.Status
			out.DeviceRegistryStatus = page.Devices.Status
			out.AreaRegistryStatus = page.Areas.Status
			out.ServicesStatus = page.Services.Status
		} else if page.Epoch != epoch || page.Entities.Status != out.EntityRegistryStatus || page.Devices.Status != out.DeviceRegistryStatus || page.Areas.Status != out.AreaRegistryStatus || page.Services.Status != out.ServicesStatus {
			return discovery.Snapshot{}, ErrProtocol
		}
		for _, e := range page.Entities.Rows {
			parts := strings.Split(e.EntityID, ".")
			if len(parts) != 2 || len(e.RegistryID) > 128 || len(e.DeviceID) > 128 || len(e.AreaID) > 128 {
				return discovery.Snapshot{}, ErrProtocol
			}
			out.Entities = append(out.Entities, discovery.Entity{Identity: discovery.NativeIdentity(e.EntityID, e.RegistryID), EntityID: e.EntityID, Name: e.Name, Domain: parts[0], Status: e.Status, Device: e.DeviceID, Area: e.AreaID, Attributes: e.Attributes})
		}
		for _, d := range page.Devices.Rows {
			if d.ID == "" || len(d.ID) > 128 {
				return discovery.Snapshot{}, ErrProtocol
			}
			out.Devices = append(out.Devices, discovery.RegistryRecord{Identity: registryIdentity("device_registry", d.ID), Name: d.Name, Area: d.AreaID, Status: d.Status})
		}
		for _, a := range page.Areas.Rows {
			if a.ID == "" || len(a.ID) > 128 {
				return discovery.Snapshot{}, ErrProtocol
			}
			out.Areas = append(out.Areas, discovery.RegistryRecord{Identity: registryIdentity("area_registry", a.ID), Name: a.Name, Status: a.Status})
		}
		out.Services = append(out.Services, page.Services.Rows...)
		if len(out.Entities) > discovery.MaxEntities || len(out.Devices) > discovery.MaxEntities || len(out.Areas) > discovery.MaxEntities || len(out.Services) > discovery.MaxServices {
			return discovery.Snapshot{}, ErrProtocol
		}
		if !page.More {
			if page.Cursor != "" {
				return discovery.Snapshot{}, ErrProtocol
			}
			normalized, normalizeErr := discovery.Normalize(out)
			if normalizeErr != nil {
				return discovery.Snapshot{}, normalizeErr
			}
			c.mu.Lock()
			owned, _ := discovery.Normalize(normalized)
			c.lastDiscovery = &owned
			c.discoveryFresh = true
			c.mu.Unlock()
			return normalized, nil
		}
		if page.Cursor == "" || page.Cursor == cursor || seenCursors[page.Cursor] {
			return discovery.Snapshot{}, ErrProtocol
		}
		seenCursors[page.Cursor] = true
		cursor = page.Cursor
	}
	return discovery.Snapshot{}, ErrProtocol
}
