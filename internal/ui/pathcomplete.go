package ui

import (
	"os"
	"sort"
	"strings"

	"github.com/phankieuphu/herd/internal/config"
)

const maxDirMatches = 200

// dirMatches splits a typed path into the part up to the last "/" and the
// names of the folders in it that start with the rest (case-insensitive).
// Hidden folders are listed only when the rest starts with ".".
func dirMatches(input string) (parent string, names []string) {
	if input == "~" {
		input = "~/"
	}
	cut := strings.LastIndex(input, "/") + 1
	parent, base := input[:cut], input[cut:]
	dir := config.Expand(parent)
	if dir == "" {
		dir = "."
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return parent, nil
	}
	lb := strings.ToLower(base)
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, ".") && !strings.HasPrefix(base, ".") {
			continue
		}
		if !strings.HasPrefix(strings.ToLower(n), lb) || !isDir(dir, e) {
			continue
		}
		names = append(names, n)
		if len(names) == maxDirMatches {
			break
		}
	}
	sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
	return parent, names
}

// isDir follows symlinks, so a link to a folder counts as a folder.
func isDir(dir string, e os.DirEntry) bool {
	if e.IsDir() {
		return true
	}
	if e.Type()&os.ModeSymlink == 0 {
		return false
	}
	st, err := os.Stat(strings.TrimSuffix(dir, "/") + "/" + e.Name())
	return err == nil && st.IsDir()
}

// commonPrefix is the longest case-insensitive prefix of names, spelled as
// in the first name.
func commonPrefix(names []string) string {
	if len(names) == 0 {
		return ""
	}
	p := []rune(names[0])
	for _, n := range names[1:] {
		r := []rune(n)
		i := 0
		for i < len(p) && i < len(r) && strings.EqualFold(string(p[i]), string(r[i])) {
			i++
		}
		p = p[:i]
	}
	return string(p)
}

// folderSuggest refreshes the folder suggestions of the new agent dialog.
func (d *dialog) folderSuggest() {
	d.cycling = false
	d.suggIdx = -1
	_, d.sugg = dirMatches(d.inputs[0].Value())
}

// folderComplete handles tab (dir=+1) and shift+tab (dir=-1) in the folder
// field. One match is filled in; several are first narrowed to their common
// prefix, then cycled. It reports false when there is nothing to complete.
func (d *dialog) folderComplete(dir int) bool {
	in := &d.inputs[0]
	if d.cycling && d.suggIdx >= 0 && in.Value() != d.cycleParent+d.sugg[d.suggIdx]+"/" {
		d.cycling = false
	}
	if d.cycling {
		d.suggIdx = (d.suggIdx + dir + len(d.sugg)) % len(d.sugg)
		in.SetValue(d.cycleParent + d.sugg[d.suggIdx] + "/")
		in.CursorEnd()
		return true
	}
	parent, names := dirMatches(in.Value())
	if len(names) == 0 {
		return false
	}
	if len(names) == 1 {
		in.SetValue(parent + names[0] + "/")
		in.CursorEnd()
		d.folderSuggest()
		return true
	}
	base := strings.TrimPrefix(in.Value(), parent)
	if p := commonPrefix(names); len([]rune(p)) > len([]rune(base)) {
		in.SetValue(parent + p)
		in.CursorEnd()
		d.sugg = names
		return true
	}
	d.sugg, d.cycling, d.cycleParent = names, true, parent
	d.suggIdx = -1
	if dir < 0 {
		d.suggIdx = 0
	}
	return d.folderComplete(dir)
}
