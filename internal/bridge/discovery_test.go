package bridge

import (
	"context"
	"encoding/json"
	"github.com/housefold/runtime/internal/discovery"
	"github.com/housefold/runtime/internal/durable"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func discoveryFixture(t *testing.T) Page {
	raw, err := os.ReadFile("testdata/discovery_response_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var r Response
	json.Unmarshal(raw, &r)
	var page Page
	if json.Unmarshal(r.Result, &page) != nil {
		t.Fatal("fixture")
	}
	return page
}
func discoveryClient(t *testing.T, pages []Page) (*Client, *fake) {
	index := 0
	f := &fake{exchange: func(ctx context.Context, command Command) ([]byte, error) {
		if command.Type == "housefold/hello" {
			r := helloFixture(t)
			r.ID = command.ID
			return json.Marshal(r)
		}
		if index >= len(pages) {
			return nil, ErrProtocol
		}
		raw, _ := json.Marshal(pages[index])
		index++
		return json.Marshal(Response{ID: command.ID, Type: "result", Success: true, Result: raw})
	}}
	c := New(f)
	if _, err := c.Probe(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	return c, f
}
func TestCanonicalBridgeDiscovery(t *testing.T) {
	page := discoveryFixture(t)
	c, _ := discoveryClient(t, []Page{page})
	snapshot, err := c.FetchDiscovery(context.Background())
	if err != nil || len(snapshot.Entities) != 2 || snapshot.Entities[0].Identity.Weak || !snapshot.Entities[1].Identity.Weak || snapshot.Devices[0].Area != "fixture_area" {
		t.Fatal(snapshot, err)
	}
	snapshot.Entities[0].Name = "mutated"
	retained, fresh := c.LastDiscovery()
	if !fresh || retained.Entities[0].Name != "Synthetic Lamp" {
		t.Fatal("ownership")
	}
	g, _ := discovery.OpenGenerator(durable.NewFile(filepath.Join(t.TempDir(), "bindings")))
	if _, err = g.Generate(retained); err != nil {
		t.Fatal("canonical generation", err)
	}
}
func TestPartialDiscoveryNeverReplaces(t *testing.T) {
	good := discoveryFixture(t)
	first := good
	first.More = true
	first.Cursor = "next"
	second := good
	second.Index = 1
	second.Epoch = "changed-epoch"
	c, _ := discoveryClient(t, []Page{good, first, second})
	if _, err := c.FetchDiscovery(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.FetchDiscovery(context.Background()); err == nil {
		t.Fatal("epoch gap")
	}
	retained, fresh := c.LastDiscovery()
	if fresh || len(retained.Entities) != 2 {
		t.Fatal("failed candidate replaced published discovery")
	}
}
func TestDistinctionsAndBounds(t *testing.T) {
	for _, status := range []discovery.Availability{discovery.Missing, discovery.Unsupported, discovery.Redacted} {
		p := discoveryFixture(t)
		p.Devices = Section[RegistryObject]{Status: status}
		p.Areas = Section[RegistryObject]{Status: status}
		p.Services = Section[discovery.Service]{Status: status}
		c, _ := discoveryClient(t, []Page{p})
		snapshot, err := c.FetchDiscovery(context.Background())
		if err != nil || snapshot.DeviceRegistryStatus != status || snapshot.AreaRegistryStatus != status || snapshot.ServicesStatus != status {
			t.Fatal(snapshot, err)
		}
	}
	for _, mutate := range []func(*Page){func(p *Page) { p.Index = 1 }, func(p *Page) { p.More = true; p.Cursor = "" }, func(p *Page) { p.Entities.Rows = append(p.Entities.Rows, p.Entities.Rows[0]) }, func(p *Page) { p.Devices.Status = discovery.Redacted }, func(p *Page) { p.Services.Rows = make([]discovery.Service, discovery.MaxServices+1) }} {
		p := discoveryFixture(t)
		mutate(&p)
		c, _ := discoveryClient(t, []Page{p})
		if _, err := c.FetchDiscovery(context.Background()); err == nil {
			t.Fatal("invalid accepted")
		}
	}
}
func TestChunkLimitAndCancellation(t *testing.T) {
	p := discoveryFixture(t)
	p.More = true
	p.Cursor = "next"
	c, _ := discoveryClient(t, []Page{p})
	c.mu.Lock()
	cap := c.result.Capabilities["discovery"]
	cap.Limits.Chunks = 1
	c.result.Capabilities["discovery"] = cap
	c.mu.Unlock()
	if _, err := c.FetchDiscovery(context.Background()); err == nil {
		t.Fatal("chunk bound")
	}
	c, _ = discoveryClient(t, []Page{discoveryFixture(t)})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.FetchDiscovery(ctx); err == nil {
		t.Fatal("canceled fetch accepted")
	}
}
func TestMultipartAndTotalBudget(t *testing.T) {
	first := discoveryFixture(t)
	second := discoveryFixture(t)
	first.Entities.Rows = first.Entities.Rows[:1]
	first.More = true
	first.Cursor = "next"
	second.Index = 1
	second.Entities.Rows = second.Entities.Rows[1:]
	second.Devices.Rows = nil
	second.Areas.Rows = nil
	second.Services.Rows = nil
	c, _ := discoveryClient(t, []Page{first, second})
	snapshot, err := c.FetchDiscovery(context.Background())
	if err != nil || len(snapshot.Entities) != 2 {
		t.Fatal(snapshot, err)
	}
	c, _ = discoveryClient(t, []Page{discoveryFixture(t)})
	c.mu.Lock()
	cap := c.result.Capabilities["discovery"]
	cap.Limits.Total = 128
	c.result.Capabilities["discovery"] = cap
	c.mu.Unlock()
	if _, err = c.FetchDiscovery(context.Background()); err == nil {
		t.Fatal("total bound ignored")
	}
}
