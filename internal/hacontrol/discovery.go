package hacontrol

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/housefold/runtime/internal/discovery"
)

type registryEntity struct {
	ID       string `json:"id"`
	EntityID string `json:"entity_id"`
	UniqueID string `json:"unique_id"`
	Platform string `json:"platform"`
	Name     string `json:"name"`
	DeviceID string `json:"device_id"`
	AreaID   string `json:"area_id"`
}
type registryDevice struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	NameByUser string `json:"name_by_user"`
	AreaID     string `json:"area_id"`
}
type registryArea struct {
	ID   string `json:"area_id"`
	Name string `json:"name"`
}

func registryAvailability(code string, err error) discovery.Availability {
	if err == nil {
		return discovery.Available
	}
	switch code {
	case "unauthorized", "not_allowed", "forbidden":
		return discovery.Redacted
	case "unknown_command", "unsupported":
		return discovery.Unsupported
	default:
		return discovery.Missing
	}
}
func (n *Native) CollectDiscovery(ctx context.Context) (err error) {
	defer func() {
		if err != nil {
			n.mu.Lock()
			n.discoveryFresh = false
			n.mu.Unlock()
		}
	}()
	if n.source == nil {
		return ErrNative
	}
	descriptors, err := n.source.Descriptors(discovery.MaxEntities)
	if err != nil {
		return ErrNative
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	out := discovery.Snapshot{Schema: 1, RegistryStatus: discovery.Missing, ServicesStatus: discovery.Missing}
	entityRaw, code, entityErr := n.core.command(ctx, "config/entity_registry/list", discovery.MaxSnapshot)
	out.EntityRegistryStatus = registryAvailability(code, entityErr)
	out.RegistryStatus = out.EntityRegistryStatus
	registry := map[string]registryEntity{}
	if entityErr == nil {
		var rows []registryEntity
		if json.Unmarshal(entityRaw, &rows) != nil || rows == nil || len(rows) > discovery.MaxEntities {
			return ErrNative
		}
		for _, row := range rows {
			if registry[row.EntityID].EntityID != "" {
				return ErrNative
			}
			registry[row.EntityID] = row
		}
	}
	deviceRaw, code, deviceErr := n.core.command(ctx, "config/device_registry/list", discovery.MaxSnapshot)
	out.DeviceRegistryStatus = registryAvailability(code, deviceErr)
	devices := map[string]registryDevice{}
	if deviceErr == nil {
		var rows []registryDevice
		if json.Unmarshal(deviceRaw, &rows) != nil || rows == nil || len(rows) > discovery.MaxEntities {
			return ErrNative
		}
		for _, row := range rows {
			if row.ID == "" || devices[row.ID].ID != "" {
				return ErrNative
			}
			devices[row.ID] = row
			name := row.Name
			if row.NameByUser != "" {
				name = row.NameByUser
			}
			out.Devices = append(out.Devices, discovery.RegistryRecord{Identity: discovery.Identity{Provider: "ha", Key: "device_registry/" + row.ID}, Name: name, Status: discovery.Available, Area: row.AreaID})
		}
	}
	areaRaw, code, areaErr := n.core.command(ctx, "config/area_registry/list", discovery.MaxSnapshot)
	out.AreaRegistryStatus = registryAvailability(code, areaErr)
	if areaErr == nil {
		var rows []registryArea
		if json.Unmarshal(areaRaw, &rows) != nil || rows == nil || len(rows) > discovery.MaxEntities {
			return ErrNative
		}
		for _, row := range rows {
			if row.ID == "" {
				return ErrNative
			}
			out.Areas = append(out.Areas, discovery.RegistryRecord{Identity: discovery.Identity{Provider: "ha", Key: "area_registry/" + row.ID}, Name: row.Name, Status: discovery.Available})
		}
	}
	included := map[string]bool{}
	appendEntity := func(id, name string, fields []discovery.Field, status discovery.Availability) error {
		parts := strings.Split(id, ".")
		if len(parts) != 2 {
			return ErrNative
		}
		row := registry[id]
		stable := row.ID
		if stable == "" && row.UniqueID != "" && row.Platform != "" {
			stable = parts[0] + "/" + row.Platform + "/" + row.UniqueID
		}
		area := row.AreaID
		if area == "" {
			area = devices[row.DeviceID].AreaID
		}
		out.Entities = append(out.Entities, discovery.Entity{Identity: discovery.NativeIdentity(id, stable), EntityID: id, Name: name, Domain: parts[0], Status: status, Device: row.DeviceID, Area: area, Attributes: fields})
		if len(out.Entities) > discovery.MaxEntities {
			return ErrNative
		}
		return nil
	}
	for _, d := range descriptors {
		fields := []discovery.Field{}
		keys := []string{}
		for key := range d.Fields {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fields = append(fields, discovery.Field{Name: key, Type: d.Fields[key]})
		}
		if err = appendEntity(d.EntityID, d.Name, fields, discovery.Available); err != nil {
			return err
		}
		included[d.EntityID] = true
	}
	for id, row := range registry {
		if !included[id] {
			if err = appendEntity(id, row.Name, nil, discovery.Missing); err != nil {
				return err
			}
		}
	}
	outcome, servicesRaw, servicesErr := n.request(ctx, "GET", "services", nil, discovery.MaxSnapshot)
	if servicesErr == nil {
		var domains []struct {
			Domain   string                     `json:"domain"`
			Services map[string]json.RawMessage `json:"services"`
		}
		if json.Unmarshal(servicesRaw, &domains) != nil || domains == nil || len(domains) > discovery.MaxServices {
			return ErrNative
		}
		for _, domain := range domains {
			for name, schema := range domain.Services {
				var declaration struct {
					Fields map[string]struct {
						Required bool                       `json:"required"`
						Selector map[string]json.RawMessage `json:"selector"`
					} `json:"fields"`
				}
				if json.Unmarshal(schema, &declaration) != nil || len(declaration.Fields) > discovery.MaxFields {
					return ErrNative
				}
				service := discovery.Service{Domain: domain.Domain, Name: name, Status: discovery.Available, Schema: schema}
				keys := []string{}
				for key := range declaration.Fields {
					keys = append(keys, key)
				}
				sort.Strings(keys)
				for _, key := range keys {
					field := declaration.Fields[key]
					typ := "any"
					for selector := range field.Selector {
						switch selector {
						case "text":
							typ = "string"
						case "number":
							typ = "number"
						case "boolean":
							typ = "bool"
						}
					}
					if typ == "bool" {
						typ = "boolean"
					}
					service.Fields = append(service.Fields, discovery.Field{Name: key, Type: typ, Required: field.Required})
				}
				out.Services = append(out.Services, service)
				if len(out.Services) > discovery.MaxServices {
					return ErrNative
				}
			}
		}
		out.ServicesStatus = discovery.Available
	} else if outcome == "rejected_by_ha" {
		out.ServicesStatus = discovery.Redacted
	}
	normalized, err := discovery.Normalize(out)
	if err != nil {
		return ErrNative
	}
	n.mu.Lock()
	n.discovered = normalized
	n.discoveryFresh = true
	sink := n.discoverySink
	n.mu.Unlock()
	if sink != nil {
		if err = sink(normalized); err != nil {
			return err
		}
	}
	return nil
}
