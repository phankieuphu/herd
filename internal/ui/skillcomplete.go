package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/charmbracelet/bubbles/textinput"

	"github.com/phankieuphu/herd/internal/config"
)

// skill is something an agent runs when its name is typed in a prompt:
// a Claude Code skill or slash command, a Codex prompt or skill.
type skill struct {
	name string // as typed: "/review", "/prompts:fix", "$lint"
	desc string
}

// skillFlavor picks where an agent kind keeps its skills: "claude",
// "codex", or "" for agents herd does not know.
func skillFlavor(kind, command string) string {
	for _, s := range []string{kind, firstWord(command)} {
		s = strings.ToLower(filepath.Base(s))
		switch {
		case strings.Contains(s, "claude"):
			return "claude"
		case strings.Contains(s, "codex"):
			return "codex"
		}
	}
	return ""
}

func firstWord(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return ""
}

// findSkills lists the skills an agent of the given flavor can use when it
// runs in dir. Project skills win over user skills with the same name.
func findSkills(flavor, dir string) []skill {
	seen := map[string]bool{}
	var out []skill
	add := func(name, desc string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, skill{name, desc})
	}
	projects := projectDirs(config.Expand(dir))
	switch flavor {
	case "claude":
		home := envDir("CLAUDE_CONFIG_DIR", "~/.claude")
		for _, base := range append(join(projects, ".claude"), home) {
			skillDirs(filepath.Join(base, "skills"), "/", add)
			commandFiles(filepath.Join(base, "commands"), "/", add)
		}
		for name, path := range claudePlugins(home) {
			skillDirs(filepath.Join(path, "skills"), "/"+name+":", add)
			commandFiles(filepath.Join(path, "commands"), "/"+name+":", add)
		}
	case "codex":
		home := envDir("CODEX_HOME", "~/.codex")
		for _, base := range append(join(projects, ".codex"), home) {
			skillDirs(filepath.Join(base, "skills"), "$", add)
		}
		commandFiles(filepath.Join(home, "prompts"), "/prompts:", add)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].name) < strings.ToLower(out[j].name) })
	return out
}

func envDir(env, def string) string {
	if v := os.Getenv(env); v != "" {
		return config.Expand(v)
	}
	return config.Expand(def)
}

func join(dirs []string, sub string) []string {
	out := make([]string, len(dirs))
	for i, d := range dirs {
		out[i] = filepath.Join(d, sub)
	}
	return out
}

// projectDirs is dir and the folders above it, nearest first, stopping
// before the home folder (whose config is read as user config).
func projectDirs(dir string) []string {
	if dir == "" {
		return nil
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil
	}
	home, _ := os.UserHomeDir()
	var out []string
	for dir != home {
		out = append(out, dir)
		up := filepath.Dir(dir)
		if up == dir {
			break
		}
		dir = up
	}
	return out
}

// skillDirs adds every <dir>/<name>/SKILL.md as prefix+name.
func skillDirs(dir, prefix string, add func(name, desc string)) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		f := filepath.Join(dir, e.Name(), "SKILL.md")
		if _, err := os.Stat(f); err == nil {
			add(prefix+e.Name(), description(f))
		}
	}
}

// commandFiles adds every .md file under dir (sub-folders included) as
// prefix+file name.
func commandFiles(dir, prefix string, add func(name, desc string)) {
	_ = filepath.WalkDir(dir, func(p string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			return nil
		}
		add(prefix+strings.TrimSuffix(e.Name(), ".md"), description(p))
		return nil
	})
}

// description reads "description:" from a file's front matter.
func description(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return ""
	}
	for _, l := range lines[1:] {
		l = strings.TrimSpace(l)
		if l == "---" {
			break
		}
		if v, ok := strings.CutPrefix(l, "description:"); ok {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}

// claudePlugins maps installed Claude Code plugin names to their folders.
func claudePlugins(home string) map[string]string {
	data, err := os.ReadFile(filepath.Join(home, "plugins", "installed_plugins.json"))
	if err != nil {
		return nil
	}
	var f struct {
		Plugins map[string]json.RawMessage `json:"plugins"`
	}
	if json.Unmarshal(data, &f) != nil {
		return nil
	}
	type inst struct {
		InstallPath string `json:"installPath"`
	}
	out := map[string]string{}
	for key, raw := range f.Plugins {
		name, _, _ := strings.Cut(key, "@")
		var one inst
		var many []inst
		switch {
		case json.Unmarshal(raw, &many) == nil && len(many) > 0:
			one = many[len(many)-1]
		case json.Unmarshal(raw, &one) == nil:
		}
		if one.InstallPath != "" {
			out[name] = config.Expand(one.InstallPath)
		}
	}
	return out
}

// skillInput is the text input of the dialog that takes skill names, or
// nil when the focused field does not.
func (d *dialog) skillInput() *textinput.Model {
	switch {
	case d.kind == dlgNew && d.focus == 3:
		return &d.inputs[2]
	case d.kind == dlgMessage && len(d.inputs) > 0:
		return &d.inputs[0]
	}
	return nil
}

// skillList returns the skills for the dialog's agent, read once per
// agent kind and folder.
func (d *dialog) skillList() []skill {
	flavor, dir := d.flavor, d.skillDir
	if d.kind == dlgNew {
		flavor = skillFlavor(d.kinds[d.kindIdx], d.kindCmds[d.kinds[d.kindIdx]])
		dir = d.value(0)
	}
	if key := flavor + "\x00" + dir; key != d.skillKey || d.skills == nil {
		d.skillKey, d.skills = key, findSkills(flavor, dir)
	}
	return d.skills
}

// skillToken finds the word that ends at the cursor when it names a skill:
// "$name" anywhere, "/name" only as the first word.
func skillToken(in *textinput.Model) (before, tok, after string, ok bool) {
	r := []rune(in.Value())
	pos := min(in.Position(), len(r))
	s := pos
	for s > 0 && !unicode.IsSpace(r[s-1]) {
		s--
	}
	tok = string(r[s:pos])
	switch {
	case strings.HasPrefix(tok, "$"):
	case strings.HasPrefix(tok, "/") && strings.TrimSpace(string(r[:s])) == "":
	default:
		return "", "", "", false
	}
	return string(r[:s]), tok, string(r[pos:]), true
}

func skillMatches(list []skill, tok string) []skill {
	var out []skill
	lt := strings.ToLower(tok)
	for _, s := range list {
		if strings.HasPrefix(strings.ToLower(s.name), lt) {
			out = append(out, s)
		}
	}
	return out
}

func setAt(in *textinput.Model, before, mid, after string) {
	in.SetValue(before + mid + after)
	in.SetCursor(len([]rune(before + mid)))
}

// skillSuggest refreshes the skill suggestions under the prompt field.
func (d *dialog) skillSuggest() {
	d.cycling = false
	d.suggIdx = -1
	d.skillSugg = nil
	in := d.skillInput()
	if in == nil {
		return
	}
	if _, tok, _, ok := skillToken(in); ok {
		d.skillSugg = skillMatches(d.skillList(), tok)
	}
}

// skillComplete handles tab (dir=+1) and shift+tab (dir=-1) on a skill
// name in the prompt, the same way folderComplete does for folders. It
// reports false when there is nothing to complete.
func (d *dialog) skillComplete(dir int) bool {
	in := d.skillInput()
	if in == nil {
		return false
	}
	if d.cycling && d.suggIdx >= 0 && in.Value() != d.cycleParent+d.skillSugg[d.suggIdx].name+d.cycleAfter {
		d.cycling = false
	}
	if d.cycling {
		d.suggIdx = (d.suggIdx + dir + len(d.skillSugg)) % len(d.skillSugg)
		setAt(in, d.cycleParent, d.skillSugg[d.suggIdx].name, d.cycleAfter)
		return true
	}
	before, tok, after, ok := skillToken(in)
	if !ok {
		return false
	}
	matches := skillMatches(d.skillList(), tok)
	switch len(matches) {
	case 0:
		return false
	case 1:
		if !strings.HasPrefix(after, " ") {
			after = " " + after
		}
		setAt(in, before, matches[0].name, after)
		in.SetCursor(len([]rune(before+matches[0].name)) + 1)
		d.skillSuggest()
		return true
	}
	names := make([]string, len(matches))
	for i, s := range matches {
		names[i] = s.name
	}
	if p := commonPrefix(names); len([]rune(p)) > len([]rune(tok)) {
		setAt(in, before, p, after)
		d.skillSugg = matches
		return true
	}
	d.skillSugg, d.cycling, d.cycleParent, d.cycleAfter = matches, true, before, after
	d.suggIdx = -1
	if dir < 0 {
		d.suggIdx = 0
	}
	return d.skillComplete(dir)
}
