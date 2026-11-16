package routing

import (
	"os"
	"path/filepath"
	"testing"

	"simple-magpie/internal/config"
)

func writeCfg(t *testing.T, s string) *config.Config {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.json")
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

const cfgJSON = `{
  "providers": [
    {"id":"a","base_url":"http://a","protocols":["chat"],"default_model":"m","accounts":[{"name":"a1","key":"k"},{"name":"a2","key":"k"}]},
    {"id":"b","base_url":"http://b","protocols":["chat"],"default_model":"m","accounts":[{"name":"b1","key":"k"}]}
  ],
  "groups": [
    {"id":"g","strategy":"order","members":[{"provider":"a","model":"m"},{"provider":"b","model":"m"}]}
  ]
}`

func TestPlanGroup(t *testing.T) {
	r := New(writeCfg(t, cfgJSON))
	cands, resolved := r.Plan("group/g", "")
	if resolved != "group/g" {
		t.Fatalf("resolved = %q", resolved)
	}
	if len(cands) != 3 {
		t.Fatalf("candidates = %d, want 3 (a:2 accounts + b:1)", len(cands))
	}
	if cands[0].Provider != "a" || cands[2].Provider != "b" {
		t.Fatalf("order wrong: %+v", cands)
	}
}

func TestPlanProviderModel(t *testing.T) {
	r := New(writeCfg(t, cfgJSON))
	cands, _ := r.Plan("a/m", "")
	if len(cands) != 2 || cands[0].Provider != "a" {
		t.Fatalf("provider/model plan wrong: %+v", cands)
	}
}

func TestAffinityPrefersLastAccount(t *testing.T) {
	r := New(writeCfg(t, cfgJSON))
	r.Served("s1", "a", "a2")
	cands, _ := r.Plan("a/m", "s1")
	if cands[0].Account.Name != "a2" {
		t.Fatalf("affinity did not put a2 first: %+v", cands)
	}
}

func TestRestPutsAccountLast(t *testing.T) {
	r := New(writeCfg(t, cfgJSON))
	r.Rest("a", "a1", 60_000_000_000) // 60s
	cands, _ := r.Plan("a/m", "")
	if cands[0].Account.Name != "a2" {
		t.Fatalf("resting account not moved back: %+v", cands)
	}
}
