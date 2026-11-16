// Package config loads simple-magpie's providers, accounts and routing groups
// from a JSON file. It is deliberately small: a provider speaks one or more
// wire protocols, has one or more accounts (keys), and a routing group is a
// named list of provider/model members with a strategy.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Account is one key for a provider. Several accounts give failover.
type Account struct {
	Name  string `json:"name"`
	Key   string `json:"key"`
	Proxy string `json:"proxy,omitempty"` // optional upstream proxy URL
}

// Provider is a vendor endpoint.
type Provider struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	BaseURL   string    `json:"base_url"`
	Protocols []string  `json:"protocols"` // what this provider speaks upstream
	Accounts  []Account `json:"accounts"`
	Weight    int       `json:"weight,omitempty"` // for weighted strategies, default 1
	// Default is the model used when a request names only the group without
	// a provider, or when this provider is a member with no model set in a
	// group. Optional.
	DefaultModel string `json:"default_model,omitempty"`
}

// Speaks reports whether p can be asked in proto upstream.
func (p Provider) Speaks(proto string) bool {
	for _, s := range p.Protocols {
		if s == proto {
			return true
		}
	}
	return false
}

// Member is one provider/model pair inside a routing group.
type Member struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Weight   int    `json:"weight,omitempty"`
}

// Group is a named set of members routed by a strategy. A model id "group/<id>"
// or the bare id selects it.
type Group struct {
	ID       string   `json:"id"`
	Name     string   `json:"name,omitempty"`
	Strategy string   `json:"strategy"` // order | rotate | usage | fastest
	Members  []Member `json:"members"`
}

// Config is the whole file.
type Config struct {
	Listen       string     `json:"listen"`        // e.g. 127.0.0.1:3425
	DefaultModel string     `json:"default_model"` // served when a request names none we know
	Providers    []Provider `json:"providers"`
	Groups       []Group    `json:"groups"`
	// Affinity: auto | off | strict. auto/session keeps a conversation on the
	// account that last answered it (prompt cache). Default auto.
	Affinity string `json:"affinity,omitempty"`

	byID map[string]*Provider
}

// Load reads and validates a config file.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	dec := json.NewDecoder(strings.NewReader(string(b)))
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := c.prepare(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) prepare() error {
	if c.Listen == "" {
		c.Listen = "127.0.0.1:3425"
	}
	if c.Affinity == "" {
		c.Affinity = "auto"
	}
	c.byID = make(map[string]*Provider, len(c.Providers))
	for i := range c.Providers {
		p := &c.Providers[i]
		if p.ID == "" {
			return fmt.Errorf("provider with empty id")
		}
		if _, dup := c.byID[p.ID]; dup {
			return fmt.Errorf("duplicate provider id %q", p.ID)
		}
		if p.Name == "" {
			p.Name = p.ID
		}
		if len(p.Protocols) == 0 {
			p.Protocols = []string{"chat"}
		}
		if p.Weight <= 0 {
			p.Weight = 1
		}
		for j := range p.Accounts {
			a := &p.Accounts[j]
			if a.Name == "" {
				a.Name = fmt.Sprintf("%s-%d", p.ID, j+1)
			}
		}
		if strings.HasSuffix(p.BaseURL, "/") {
			p.BaseURL = strings.TrimRight(p.BaseURL, "/")
		}
		c.byID[p.ID] = p
	}
	for i := range c.Groups {
		g := &c.Groups[i]
		if g.ID == "" {
			return fmt.Errorf("group with empty id")
		}
		if g.Strategy == "" {
			g.Strategy = "order"
		}
		for _, m := range g.Members {
			if _, ok := c.byID[m.Provider]; !ok {
				return fmt.Errorf("group %q names unknown provider %q", g.ID, m.Provider)
			}
		}
	}
	return nil
}

// Provider returns a provider by id.
func (c *Config) Provider(id string) (*Provider, bool) {
	p, ok := c.byID[id]
	return p, ok
}

// Group returns a routing group by id.
func (c *Config) Group(id string) (*Group, bool) {
	for i := range c.Groups {
		if c.Groups[i].ID == id {
			return &c.Groups[i], true
		}
	}
	return nil, false
}

// ProviderIDs lists providers sorted, for the UI.
func (c *Config) ProviderIDs() []string {
	out := make([]string, 0, len(c.byID))
	for id := range c.byID {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
