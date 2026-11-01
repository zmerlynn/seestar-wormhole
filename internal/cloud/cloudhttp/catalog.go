package cloudhttp

import (
	"encoding/json/v2"
	"fmt"
	"strings"
)

// CatalogPair contains escaped model text and the full displayed serial.
// Pairs are bounded to keep one encoded log record below the 16 KiB catalog record budget.
type CatalogPair struct {
	Model  string `json:"model"`
	Serial string `json:"serial"`
}
type Catalog struct {
	Pairs    []CatalogPair `json:"devices"`
	Count    int           `json:"count"`
	Complete bool          `json:"complete"`
	Omitted  int           `json:"omitted"`
}

func newCatalog(devices []Device, complete bool) Catalog {
	c := Catalog{Pairs: []CatalogPair{}, Count: len(devices), Complete: complete}
	for _, d := range devices {
		pair := CatalogPair{Model: displayModel(d.Model), Serial: displaySerial(d.Serial)}
		candidate := append(c.Pairs, pair)
		b, _ := json.Marshal(candidate)
		if len(b) > 12<<10 {
			break
		}
		c.Pairs = candidate
	}
	c.Omitted = c.Count - len(c.Pairs)
	return c
}

// Message is the sole catalog lifecycle template. Whole labels and escapes are
// retained; the suffix counts every pair omitted from the bounded event line.
func (c Catalog) Message() string {
	prefix := "Account telescopes: "
	if !c.Complete && c.Count != 0 {
		prefix = "Account telescopes (list may be incomplete): "
	}
	if c.Count == 0 {
		return prefix + "none"
	}
	labels := []string{}
	for _, p := range c.Pairs {
		model := p.Model
		if model == "" {
			model = "Seestar"
		}
		label := model + " " + p.Serial
		if len(prefix)+len(strings.Join(append(labels, label), ", "))+32 > 4096 {
			break
		}
		labels = append(labels, label)
	}
	if more := c.Count - len(labels); more > 0 {
		labels = append(labels, fmt.Sprintf("… and %d more", more))
	}
	return prefix + strings.Join(labels, ", ")
}

// SetCatalogObserver installs a synchronous observer before selection starts.
// It receives the same bounded display projection as the INFO record.
func (c *Client) SetCatalogObserver(observer func(Catalog)) { c.catalogObserver = observer }
