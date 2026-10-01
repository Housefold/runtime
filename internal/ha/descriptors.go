package ha

import (
	"encoding/json"
	"errors"
	"regexp"
	"sort"
)

// Descriptors projects names and field types without copying household values.
// Generic state access remains separate and retains unknown attributes/domains.
type Descriptor struct {
	EntityID, Name string
	Fields         map[string]string
}

var descriptorField = regexp.MustCompile(`^[a-z0-9_]{1,128}$`)
var ErrDescriptors = errors.New("HA descriptor limit or freshness unavailable")

func (s *StateSession) Descriptors(limit int) ([]Descriptor, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.metadata.Fresh || len(s.states) > limit || limit < 1 || limit > 1024 {
		return nil, ErrDescriptors
	}
	out := make([]Descriptor, 0, len(s.states))
	for id, entity := range s.states {
		d := Descriptor{EntityID: id, Name: id, Fields: map[string]string{}}
		if name, ok := entity.Attributes["friendly_name"].(string); ok && len(name) <= 256 {
			d.Name = name
		}
		keys := make([]string, 0, len(entity.Attributes))
		for key := range entity.Attributes {
			if descriptorField.MatchString(key) {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			if len(d.Fields) >= 32 {
				break
			}
			typ := "any"
			switch entity.Attributes[key].(type) {
			case string:
				typ = "string"
			case bool:
				typ = "boolean"
			case float64, json.Number:
				typ = "number"
			}
			d.Fields[key] = typ
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].EntityID < out[j].EntityID })
	return out, nil
}
