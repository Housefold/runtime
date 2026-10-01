// Package discovery defines a transport-independent, bounded HA discovery model.
package discovery

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode"
)

const MaxSnapshot = 1 << 20
const MaxEntities = 512
const MaxServices = 256
const MaxFields = 32

var ErrInvalid = errors.New("invalid discovery or binding data")

type Availability string

const (
	Available   Availability = "available"
	Missing     Availability = "missing"
	Unsupported Availability = "unsupported"
	Redacted    Availability = "permission_redacted"
)

type Identity struct {
	Provider string `json:"provider"`
	Key      string `json:"key"`
	Weak     bool   `json:"weak"`
}
type Field struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
}
type Entity struct {
	Identity   Identity     `json:"identity"`
	EntityID   string       `json:"entity_id"`
	Name       string       `json:"name"`
	Domain     string       `json:"domain"`
	Status     Availability `json:"status"`
	Device     string       `json:"device,omitempty"`
	Area       string       `json:"area,omitempty"`
	Attributes []Field      `json:"attributes,omitempty"`
}
type Service struct {
	Domain string          `json:"domain"`
	Name   string          `json:"name"`
	Status Availability    `json:"status"`
	Fields []Field         `json:"fields,omitempty"`
	Schema json.RawMessage `json:"schema,omitempty"`
}
type RegistryRecord struct {
	Identity Identity     `json:"identity"`
	Name     string       `json:"name"`
	Status   Availability `json:"status"`
	Area     string       `json:"area,omitempty"`
}
type Snapshot struct {
	EntityRegistryStatus Availability     `json:"entity_registry_status,omitempty"`
	DeviceRegistryStatus Availability     `json:"device_registry_status,omitempty"`
	AreaRegistryStatus   Availability     `json:"area_registry_status,omitempty"`
	Schema               int              `json:"schema"`
	Entities             []Entity         `json:"entities"`
	Services             []Service        `json:"services"`
	Devices              []RegistryRecord `json:"devices,omitempty"`
	Areas                []RegistryRecord `json:"areas,omitempty"`
	RegistryStatus       Availability     `json:"registry_status"`
	ServicesStatus       Availability     `json:"services_status"`
}

var identifier = regexp.MustCompile(`^[a-z0-9_]{1,128}$`)

func status(v Availability) bool {
	return v == Available || v == Missing || v == Unsupported || v == Redacted
}
func text(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}
func validIdentity(id Identity) bool {
	return id.Provider == "ha" && len(id.Key) > 0 && len(id.Key) <= 256 && text(id.Key) == id.Key
}
func key(id Identity) string {
	kind := "stable"
	if id.Weak {
		kind = "weak"
	}
	return id.Provider + ":" + kind + ":" + id.Key
}
func fields(rows []Field) bool {
	if len(rows) > MaxFields {
		return false
	}
	seen := map[string]bool{}
	for _, f := range rows {
		if !identifier.MatchString(f.Name) || seen[f.Name] || len(f.Type) > 64 {
			return false
		}
		seen[f.Name] = true
	}
	return true
}

// Normalize deep-copies data, strips display control characters and validates
// provider facts. Missing/redacted/unsupported never become successful empties.
func Normalize(in Snapshot) (Snapshot, error) {
	raw, err := json.Marshal(in)
	if err != nil || len(raw) > MaxSnapshot {
		return Snapshot{}, ErrInvalid
	}
	var out Snapshot
	if json.Unmarshal(raw, &out) != nil || out.Schema != 1 || !status(out.RegistryStatus) || !status(out.ServicesStatus) || len(out.Entities) > MaxEntities || len(out.Services) > MaxServices || len(out.Devices) > MaxEntities || len(out.Areas) > MaxEntities {
		return Snapshot{}, ErrInvalid
	}
	for _, value := range []*Availability{&out.EntityRegistryStatus, &out.DeviceRegistryStatus, &out.AreaRegistryStatus} {
		if *value == "" {
			*value = out.RegistryStatus
		}
		if !status(*value) {
			return Snapshot{}, ErrInvalid
		}
	}
	seen := map[string]bool{}
	entityIDs := map[string]bool{}
	for i, e := range out.Entities {
		parts := strings.Split(e.EntityID, ".")
		if len(parts) != 2 || !identifier.MatchString(parts[0]) || !identifier.MatchString(parts[1]) || e.Domain != parts[0] || !validIdentity(e.Identity) || !status(e.Status) || seen[key(e.Identity)] || entityIDs[e.EntityID] || len(e.Name) > 256 || len(e.Device) > 256 || len(e.Area) > 256 || !fields(e.Attributes) {
			return Snapshot{}, ErrInvalid
		}
		seen[key(e.Identity)] = true
		entityIDs[e.EntityID] = true
		out.Entities[i].Name = text(e.Name)
	}
	seen = map[string]bool{}
	for _, s := range out.Services {
		k := s.Domain + "." + s.Name
		if !identifier.MatchString(s.Domain) || !identifier.MatchString(s.Name) || !status(s.Status) || seen[k] || !fields(s.Fields) || len(s.Schema) > 64<<10 {
			return Snapshot{}, ErrInvalid
		}
		seen[k] = true
	}
	for _, rows := range [][]RegistryRecord{out.Devices, out.Areas} {
		seen = map[string]bool{}
		for i, r := range rows {
			if !validIdentity(r.Identity) || !status(r.Status) || seen[key(r.Identity)] || len(r.Name) > 256 || len(r.Area) > 256 {
				return Snapshot{}, ErrInvalid
			}
			seen[key(r.Identity)] = true
			rows[i].Name = text(r.Name)
		}
	}
	return out, nil
}

// NativeIdentity is strong only when the provider supplies a registry key.
func NativeIdentity(entityID, registryID string) Identity {
	if registryID != "" {
		return Identity{Provider: "ha", Key: "entity_registry/" + registryID}
	}
	return Identity{Provider: "ha", Key: "entity/" + entityID, Weak: true}
}
