package tmux

import "testing"

func TestParseVersion(t *testing.T) {
	cases := map[string][2]int{
		"tmux 3.4\n":     {3, 4},
		"tmux 3.2a":      {3, 2},
		"tmux next-3.5":  {3, 5},
		"tmux master":    {0, 0},
		"tmux openbsd-7": {7, 0},
	}
	for in, want := range cases {
		ma, mi, err := ParseVersion(in)
		if want == [2]int{0, 0} {
			if err == nil {
				t.Errorf("%q: want error", in)
			}
			continue
		}
		if err != nil || ma != want[0] || mi != want[1] {
			t.Errorf("%q: got %d.%d %v", in, ma, mi, err)
		}
	}
}

func TestQuote(t *testing.T) {
	cases := map[string]string{
		"":              "''",
		"/usr/bin/herd": "/usr/bin/herd",
		"fix the bug":   "'fix the bug'",
		"it's":          `'it'\''s'`,
		"$(rm -rf /)":   "'$(rm -rf /)'",
		"a;b":           "'a;b'",
	}
	for in, want := range cases {
		if got := Quote(in); got != want {
			t.Errorf("Quote(%q) = %s want %s", in, got, want)
		}
	}
}
