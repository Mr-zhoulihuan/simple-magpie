// Package routing plans which provider/account a request tries, in order, and
// tracks per-account cooldowns and per-session affinity.
package routing

import (
	"sort"
	"sync"
	"time"

	"simple-magpie/internal/config"
)

// Candidate is one provider/model/account to try.
type Candidate struct {
	Provider string
	Model    string
	Account  config.Account
}

// Router holds the runtime state routing needs.
type Router struct {
	cfg *config.Config

	mu       sync.Mutex
	cooldown map[string]time.Time // account key -> until
	affinity map[string]string    // session -> account key that answered
	usage    map[string]int       // account key -> requests served
}

// New makes a router over cfg.
func New(cfg *config.Config) *Router {
	return &Router{
		cfg:      cfg,
		cooldown: map[string]time.Time{},
		affinity: map[string]string{},
		usage:    map[string]int{},
	}
}

func accountKey(provider, account string) string { return provider + "/" + account }

// Rest rests an account for d after a retryable failure.
func (r *Router) Rest(provider, account string, d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	until := time.Now().Add(d)
	k := accountKey(provider, account)
	if until.After(r.cooldown[k]) {
		r.cooldown[k] = until
	}
}

// Served records that an account answered a session, for affinity and usage.
func (r *Router) Served(session, provider, account string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.usage[accountKey(provider, account)]++
	if session != "" {
		r.affinity[session] = accountKey(provider, account)
	}
}

func (r *Router) resting(key string) bool {
	until, ok := r.cooldown[key]
	if !ok {
		return false
	}
	if time.Now().After(until) {
		delete(r.cooldown, key)
		return false
	}
	return true
}

// Plan returns the ordered candidates a request for model tries. model may be
// "group/<id>", a bare group id, "provider/model", or a bare model served by a
// provider whose DefaultModel matches.
func (r *Router) Plan(model, session string) ([]Candidate, string) {
	// a routing group
	if g, ok := r.group(model); ok {
		return r.planGroup(g, session), "group/" + g.ID
	}
	// provider/model
	if p, m, ok := r.splitProviderModel(model); ok {
		return r.planProvider(p, m, session), model
	}
	// bare model: find providers that serve it (by default model or any)
	var cands []Candidate
	for _, id := range r.cfg.ProviderIDs() {
		p, _ := r.cfg.Provider(id)
		if p.DefaultModel == model || model == "" {
			cands = append(cands, r.providerCandidates(p, orModel(model, p.DefaultModel), session)...)
		}
	}
	if len(cands) == 0 && model == "" {
		// last resort: the default model on its provider
		if p, ok := r.cfg.Provider(defaultProvider(r.cfg)); ok {
			cands = append(cands, r.providerCandidates(p, p.DefaultModel, session)...)
		}
	}
	return r.order(cands, "order"), model
}

func orModel(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func defaultProvider(c *config.Config) string {
	ids := c.ProviderIDs()
	if len(ids) == 0 {
		return ""
	}
	return ids[0]
}

func (r *Router) group(model string) (*config.Group, bool) {
	if len(model) > 6 && model[:6] == "group/" {
		return r.cfg.Group(model[6:])
	}
	if g, ok := r.cfg.Group(model); ok {
		return g, true
	}
	return nil, false
}

func (r *Router) splitProviderModel(model string) (*config.Provider, string, bool) {
	for i := 0; i < len(model); i++ {
		if model[i] == '/' {
			if p, ok := r.cfg.Provider(model[:i]); ok {
				return p, model[i+1:], true
			}
		}
	}
	return nil, "", false
}

func (r *Router) planGroup(g *config.Group, session string) []Candidate {
	var cands []Candidate
	for _, m := range g.Members {
		p, ok := r.cfg.Provider(m.Provider)
		if !ok {
			continue
		}
		model := m.Model
		if model == "" {
			model = p.DefaultModel
		}
		cands = append(cands, r.providerCandidates(p, model, session)...)
	}
	return r.order(cands, g.Strategy)
}

func (r *Router) planProvider(p *config.Provider, model string, session string) []Candidate {
	if model == "" {
		model = p.DefaultModel
	}
	return r.order(r.providerCandidates(p, model, session), "order")
}

// providerCandidates expands a provider to one candidate per usable account,
// affinity first, then in account order, resting ones last.
func (r *Router) providerCandidates(p *config.Provider, model string, session string) []Candidate {
	r.mu.Lock()
	pref := ""
	if r.cfg.Affinity != "off" {
		pref = r.affinity[session]
	}
	var out, rest []Candidate
	for _, a := range p.Accounts {
		c := Candidate{Provider: p.ID, Model: model, Account: a}
		if r.resting(accountKey(p.ID, a.Name)) {
			rest = append(rest, c)
		} else if accountKey(p.ID, a.Name) == pref {
			out = append([]Candidate{c}, out...)
		} else {
			out = append(out, c)
		}
	}
	r.mu.Unlock()
	return append(out, rest...)
}

// order applies a strategy to the candidate list.
func (r *Router) order(cands []Candidate, strategy string) []Candidate {
	switch strategy {
	case "rotate":
		r.mu.Lock()
		defer r.mu.Unlock()
		if len(cands) > 1 {
			cands = append(cands[1:], cands[0])
		}
		return cands
	case "usage":
		r.mu.Lock()
		defer r.mu.Unlock()
		sort.SliceStable(cands, func(i, j int) bool {
			return r.usage[accountKey(cands[i].Provider, cands[i].Account.Name)] <
				r.usage[accountKey(cands[j].Provider, cands[j].Account.Name)]
		})
		return cands
	case "weighted":
		return weighted(cands, r.cfg)
	default: // order
		return cands
	}
}

func weighted(cands []Candidate, cfg *config.Config) []Candidate {
	w := func(c Candidate) int {
		if p, ok := cfg.Provider(c.Provider); ok && p.Weight > 0 {
			return p.Weight
		}
		return 1
	}
	out := append([]Candidate(nil), cands...)
	sort.SliceStable(out, func(i, j int) bool { return w(out[i]) > w(out[j]) })
	return out
}
