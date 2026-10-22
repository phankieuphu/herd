// Package limits reads the usage-limit file used by Harp's "limit rings".
//
// The file is one JSON object. A top-level key is a provider when its value
// is an object with at least one of _ts, used5h or usedWeek. Keys that start
// with "_" are ignored.
package limits

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"time"
)

// StaleAfter is how old a reading may be before it is shown as stale.
const StaleAfter = 15 * time.Minute

// Window is one limit window (5 hours or one week).
type Window struct {
	Used  float64 // percent, 0..100
	Valid bool    // false: missing number or reset time in the past
	Reset time.Time
}

// Provider is one provider's reading.
type Provider struct {
	Key   string
	Name  string
	Stale bool
	Tier  string
	Five  Window
	Week  Window
}

type raw struct {
	Name      *string  `json:"name"`
	OK        *bool    `json:"ok"`
	Stale     *bool    `json:"stale"`
	TS        *float64 `json:"_ts"`
	Used5h    *float64 `json:"used5h"`
	Reset5h   *float64 `json:"reset5h"`
	UsedWeek  *float64 `json:"usedWeek"`
	ResetWeek *float64 `json:"resetWeek"`
	Tier      *string  `json:"tier"`
}

func window(used, reset *float64, now time.Time) Window {
	if used == nil {
		return Window{}
	}
	w := Window{Used: clamp(*used), Valid: true}
	if reset != nil {
		w.Reset = time.Unix(int64(*reset), 0)
		if w.Reset.Before(now) {
			w.Valid = false
		}
	}
	return w
}

func clamp(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// Parse parses the file contents.
func Parse(data []byte, now time.Time) ([]Provider, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, err
	}
	var out []Provider
	for key, msg := range top {
		if strings.HasPrefix(key, "_") || len(msg) == 0 || msg[0] != '{' {
			continue
		}
		var r raw
		if err := json.Unmarshal(msg, &r); err != nil {
			continue
		}
		if r.TS == nil && r.Used5h == nil && r.UsedWeek == nil {
			continue
		}
		p := Provider{Key: key, Name: key}
		if r.Name != nil && *r.Name != "" {
			p.Name = *r.Name
		}
		if r.Tier != nil {
			p.Tier = *r.Tier
		}
		switch {
		case r.TS == nil:
			p.Stale = true
		case now.Sub(time.Unix(int64(*r.TS), 0)) > StaleAfter:
			p.Stale = true
		}
		if r.OK != nil && !*r.OK {
			p.Stale = true
		}
		if r.Stale != nil && *r.Stale {
			p.Stale = true
		}
		p.Five = window(r.Used5h, r.Reset5h, now)
		p.Week = window(r.UsedWeek, r.ResetWeek, now)
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// Reader re-reads the file only when it changes.
type Reader struct {
	Path    string
	modTime time.Time
	size    int64
	last    []Provider
	lastErr error
	loaded  time.Time
}

// Read returns providers, re-parsing when the file changed. It also
// re-evaluates staleness at least once a minute.
func (r *Reader) Read(now time.Time) ([]Provider, error) {
	st, err := os.Stat(r.Path)
	if err != nil {
		r.last, r.lastErr = nil, err
		return nil, err
	}
	if st.ModTime().Equal(r.modTime) && st.Size() == r.size && now.Sub(r.loaded) < time.Minute {
		return r.last, r.lastErr
	}
	data, err := os.ReadFile(r.Path)
	if err != nil {
		r.last, r.lastErr = nil, err
		return nil, err
	}
	r.modTime, r.size, r.loaded = st.ModTime(), st.Size(), now
	r.last, r.lastErr = Parse(data, now)
	return r.last, r.lastErr
}
