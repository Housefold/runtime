package discovery

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/housefold/runtime/internal/durable"
	"go/format"
	"go/token"
	"os"
	"sort"
	"strings"
	"sync"
	"unicode"
)

const MaxBindings = 1024

type Binding struct {
	Symbol   string
	EntityID string
}
type Manifest struct {
	Version  int
	Bindings map[string]Binding
}
type Generator struct {
	mu       sync.Mutex
	store    durable.Store
	manifest Manifest
}

func OpenGenerator(store durable.Store) (*Generator, error) {
	raw, err := store.Load()
	m := Manifest{Version: 1, Bindings: map[string]Binding{}}
	if errors.Is(err, os.ErrNotExist) {
		initial, _ := json.Marshal(m)
		if err = store.Save(initial); err != nil {
			return nil, err
		}
	} else {
		if err != nil {
			return nil, err
		}
		if json.Unmarshal(raw, &m) != nil || m.Version != 1 || m.Bindings == nil || len(m.Bindings) > MaxBindings {
			return nil, durable.ErrCorrupt
		}
	}
	seen := map[string]bool{}
	for k, b := range m.Bindings {
		if len(k) > 512 || !validSymbol(b.Symbol) || seen[b.Symbol] {
			return nil, durable.ErrCorrupt
		}
		seen[b.Symbol] = true
	}
	return &Generator{store: store, manifest: m}, nil
}
func validSymbol(s string) bool {
	return len(s) > 0 && len(s) <= 128 && token.IsIdentifier(s) && token.Lookup(s) == token.IDENT && unicode.IsUpper([]rune(s)[0])
}
func symbol(s string) string {
	var b strings.Builder
	upper := true
	for _, r := range s {
		if r > 127 || !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			upper = true
			continue
		}
		if b.Len() == 0 && unicode.IsDigit(r) {
			b.WriteString("Binding")
		}
		if upper {
			r = unicode.ToUpper(r)
		}
		b.WriteRune(r)
		upper = false
	}
	if b.Len() == 0 {
		return "Binding"
	}
	v := b.String()
	if len(v) > 96 {
		v = v[:96]
	}
	return v
}
func copyManifest(m Manifest) Manifest {
	out := Manifest{Version: 1, Bindings: map[string]Binding{}}
	for k, v := range m.Bindings {
		out.Bindings[k] = v
	}
	return out
}
func bind(m *Manifest, k, name, entityID string) (string, error) {
	if b, ok := m.Bindings[k]; ok {
		b.EntityID = entityID
		m.Bindings[k] = b
		return b.Symbol, nil
	}
	if len(m.Bindings) >= MaxBindings {
		return "", ErrInvalid
	}
	v := symbol(name)
	occupied := func(s string) bool {
		for _, b := range m.Bindings {
			if b.Symbol == s {
				return true
			}
		}
		return s == "EntityRef" || s == "ServiceRef" || s == "GenericEntity" || s == "GenericService"
	}
	if occupied(v) {
		sum := sha256.Sum256([]byte(k))
		v += hex.EncodeToString(sum[:4])
	}
	if occupied(v) || !validSymbol(v) {
		return "", ErrInvalid
	}
	m.Bindings[k] = Binding{Symbol: v, EntityID: entityID}
	return v, nil
}
func goType(t string) string {
	switch t {
	case "string":
		return "string"
	case "number":
		return "float64"
	case "boolean":
		return "bool"
	default:
		return "any"
	}
}
func writeFields(b *bytes.Buffer, rows []Field) error {
	rows = append([]Field(nil), rows...)
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	seen := map[string]bool{}
	for _, f := range rows {
		name := symbol(f.Name)
		if seen[name] {
			return ErrInvalid
		}
		seen[name] = true
		typ := goType(f.Type)
		if !f.Required && typ != "any" {
			typ = "*" + typ
		}
		fmt.Fprintf(b, "%s %s `json:%q`\n", name, typ, f.Name)
	}
	return nil
}
func (g *Generator) Generate(snapshot Snapshot) ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	s, err := Normalize(snapshot)
	if err != nil {
		return nil, err
	}
	sort.Slice(s.Entities, func(i, j int) bool { return key(s.Entities[i].Identity) < key(s.Entities[j].Identity) })
	sort.Slice(s.Services, func(i, j int) bool {
		return s.Services[i].Domain+"."+s.Services[i].Name < s.Services[j].Domain+"."+s.Services[j].Name
	})
	m := copyManifest(g.manifest)
	var b bytes.Buffer
	b.WriteString("// Code generated from synthetic or local discovery; do not publish private-home data.\npackage bindings\ntype EntityRef struct{Provider,Key,EntityID string;Weak bool}\ntype ServiceRef struct{Domain,Name string}\nfunc GenericEntity(provider,key,entityID string,weak bool)EntityRef{return EntityRef{provider,key,entityID,weak}}\nfunc GenericService(domain,name string)ServiceRef{return ServiceRef{domain,name}}\n")
	symbols := map[string]bool{"EntityRef": true, "ServiceRef": true, "GenericEntity": true, "GenericService": true}
	reserve := func(v string) bool {
		if symbols[v] {
			return false
		}
		symbols[v] = true
		return true
	}
	for _, e := range s.Entities {
		if e.Status != Available {
			continue
		}
		v, err := bind(&m, key(e.Identity), e.Name, e.EntityID)
		if err != nil || !reserve(v) || !reserve(v+"Attributes") {
			return nil, ErrInvalid
		}
		fmt.Fprintf(&b, "var %s=EntityRef{Provider:%q,Key:%q,EntityID:%q,Weak:%t}\ntype %sAttributes struct {\n", v, e.Identity.Provider, e.Identity.Key, e.EntityID, e.Identity.Weak, v)
		if err = writeFields(&b, e.Attributes); err != nil {
			return nil, err
		}
		b.WriteString("}\n")
	}
	for _, s := range s.Services {
		if s.Status != Available {
			continue
		}
		v, err := bind(&m, "service:"+s.Domain+"."+s.Name, s.Domain+"_"+s.Name, "")
		if err != nil || !reserve(v) || !reserve(v+"Request") {
			return nil, ErrInvalid
		}
		fmt.Fprintf(&b, "var %s=ServiceRef{Domain:%q,Name:%q}\ntype %sRequest struct {\n", v, s.Domain, s.Name, v)
		if err = writeFields(&b, s.Fields); err != nil {
			return nil, err
		}
		b.WriteString("}\n")
	}
	source, err := format.Source(b.Bytes())
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(m)
	if err = g.store.Save(raw); err != nil {
		return nil, err
	}
	g.manifest = m
	return source, nil
}
func (g *Generator) Rename(identityKey, newSymbol string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !validSymbol(newSymbol) {
		return ErrInvalid
	}
	m := copyManifest(g.manifest)
	b, ok := m.Bindings[identityKey]
	if !ok {
		return ErrInvalid
	}
	for k, v := range m.Bindings {
		if k != identityKey && v.Symbol == newSymbol {
			return ErrInvalid
		}
	}
	b.Symbol = newSymbol
	m.Bindings[identityKey] = b
	raw, _ := json.Marshal(m)
	if err := g.store.Save(raw); err != nil {
		return err
	}
	g.manifest = m
	return nil
}
