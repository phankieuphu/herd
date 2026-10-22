package limits

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	now := time.Unix(1760000000, 0)
	data := `{
	  "_version": 1,
	  "_meta": {"_ts": 1},
	  "zeta": {"_ts": 1760000000, "used5h": 10},
	  "claude": {"name": "Claude", "ok": true, "_ts": 1760000000, "used5h": 42.5, "reset5h": 1760010000,
	             "usedWeek": 18, "resetWeek": 1760400000, "tier": "max"},
	  "codex": {"ok": true, "_ts": 1760000000, "used5h": 90, "reset5h": 1759999000},
	  "old": {"_ts": 1759990000, "used5h": 5},
	  "failed": {"ok": false, "_ts": 1760000000, "used5h": 5},
	  "flagged": {"stale": true, "_ts": 1760000000, "used5h": 5},
	  "nots": {"used5h": 120},
	  "noise": {"foo": 1},
	  "scalar": 3
	}`
	ps, err := Parse([]byte(data), now)
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for _, p := range ps {
		keys = append(keys, p.Key)
	}
	want := []string{"claude", "codex", "failed", "flagged", "nots", "old", "zeta"}
	if len(keys) != len(want) {
		t.Fatalf("keys %v", keys)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("keys %v", keys)
		}
	}
	by := map[string]Provider{}
	for _, p := range ps {
		by[p.Key] = p
	}
	c := by["claude"]
	if c.Name != "Claude" || c.Stale || !c.Five.Valid || c.Five.Used != 42.5 || c.Week.Used != 18 || c.Tier != "max" {
		t.Errorf("claude %+v", c)
	}
	if by["codex"].Name != "codex" || by["codex"].Five.Valid {
		t.Errorf("codex: reset in the past should hide the number: %+v", by["codex"])
	}
	if by["codex"].Week.Valid {
		t.Error("codex week missing should be invalid")
	}
	for _, k := range []string{"old", "failed", "flagged", "nots"} {
		if !by[k].Stale {
			t.Errorf("%s should be stale", k)
		}
	}
	if by["nots"].Five.Used != 100 {
		t.Errorf("clamp: %v", by["nots"].Five.Used)
	}
}

func TestReader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "l.json")
	now := time.Now()
	os.WriteFile(path, []byte(`{"a":{"_ts":`+itoa(now.Unix())+`,"used5h":1}}`), 0o644)
	r := &Reader{Path: path}
	ps, err := r.Read(now)
	if err != nil || len(ps) != 1 {
		t.Fatal(ps, err)
	}
	os.WriteFile(path, []byte(`{"a":{"_ts":`+itoa(now.Unix())+`,"used5h":1},"b":{"_ts":1}}`), 0o644)
	os.Chtimes(path, now.Add(time.Second), now.Add(time.Second))
	ps, _ = r.Read(now)
	if len(ps) != 2 {
		t.Fatalf("not re-read: %v", ps)
	}
	if _, err := (&Reader{Path: filepath.Join(dir, "missing")}).Read(now); err == nil {
		t.Fatal("want error for missing file")
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
