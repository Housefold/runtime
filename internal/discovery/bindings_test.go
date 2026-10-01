package discovery

import (
	"bytes"
	"github.com/housefold/runtime/internal/durable"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"testing"
)

func synthetic() Snapshot {
	return Snapshot{Schema: 1, RegistryStatus: Available, ServicesStatus: Available, Entities: []Entity{{Identity: NativeIdentity("light.synthetic", "registry_fixture"), EntityID: "light.synthetic", Name: "Synthetic Lamp", Domain: "light", Status: Available, Attributes: []Field{{Name: "brightness", Type: "number"}}}, {Identity: NativeIdentity("new_domain.fixture", ""), EntityID: "new_domain.fixture", Name: "Unknown Fixture", Domain: "new_domain", Status: Available}}, Services: []Service{{Domain: "light", Name: "turn_on", Status: Available, Fields: []Field{{Name: "brightness", Type: "number"}, {Name: "extra", Type: "unknown"}}}}}
}
func TestStableRenameDeterministicAndGeneric(t *testing.T) {
	store := durable.NewFile(filepath.Join(t.TempDir(), "bindings"))
	g, _ := OpenGenerator(store)
	s := synthetic()
	first, err := g.Generate(s)
	if err != nil {
		t.Fatal(err)
	}
	again, err := g.Generate(s)
	if err != nil || !bytes.Equal(first, again) {
		t.Fatal("nondeterministic", err)
	}
	s.Entities[0].EntityID = "light.renamed_fixture"
	s.Entities[0].Name = "Renamed Fixture"
	restored, _ := OpenGenerator(store)
	source, err := restored.Generate(s)
	if err != nil || !bytes.Contains(source, []byte("var SyntheticLamp")) || !bytes.Contains(source, []byte("light.renamed_fixture")) || !bytes.Contains(source, []byte("Weak: true")) || !bytes.Contains(source, []byte("GenericService")) {
		t.Fatal(string(source), err)
	}
	fileset := token.NewFileSet()
	file, err := parser.ParseFile(fileset, "bindings.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = (&types.Config{}).Check("bindings", fileset, []*ast.File{file}, nil); err != nil {
		t.Fatal(err)
	}
	if err = restored.Rename(key(s.Entities[0].Identity), "ExplicitName"); err != nil {
		t.Fatal(err)
	}
	source, err = restored.Generate(s)
	if err != nil || !bytes.Contains(source, []byte("var ExplicitName")) {
		t.Fatal(err)
	}
}
func TestSanitizationOwnershipAndStatus(t *testing.T) {
	s := synthetic()
	s.Entities[0].Name = "Synthetic\x00Lamp"
	out, err := Normalize(s)
	if err != nil || out.Entities[0].Name != "SyntheticLamp" {
		t.Fatal(out, err)
	}
	out.Entities[0].Attributes[0].Name = "mutated"
	if s.Entities[0].Attributes[0].Name != "brightness" {
		t.Fatal("ownership")
	}
	for _, availability := range []Availability{Missing, Unsupported, Redacted} {
		s := synthetic()
		s.RegistryStatus = availability
		s.ServicesStatus = availability
		out, err := Normalize(s)
		if err != nil || out.RegistryStatus != availability {
			t.Fatal(err)
		}
	}
	s = synthetic()
	s.Entities = append(s.Entities, s.Entities[0])
	if _, err := Normalize(s); err != ErrInvalid {
		t.Fatal("duplicate identity")
	}
}
func TestSymbolsAndBounds(t *testing.T) {
	s := synthetic()
	s.Entities[0].Name = "1 / +"
	g, _ := OpenGenerator(durable.NewFile(filepath.Join(t.TempDir(), "binding")))
	if _, err := g.Generate(s); err != nil {
		t.Fatal(err)
	}
	s.Entities[0].Attributes = []Field{{Name: "same_name"}, {Name: "same__name"}}
	if _, err := g.Generate(s); err != ErrInvalid {
		t.Fatal("field collision")
	}
	s = synthetic()
	s.Entities = make([]Entity, MaxEntities+1)
	if _, err := Normalize(s); err != ErrInvalid {
		t.Fatal(err)
	}
}
