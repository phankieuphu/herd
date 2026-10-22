package gitwt

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Fix Auth Bug!": "fix-auth-bug",
		"  ":            "agent",
		"api/v2 tests":  "api-v2-tests",
		"Tiếng Việt ok": "ti-ng-vi-t-ok",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q want %q", in, got, want)
		}
	}
}

func TestParseShortstat(t *testing.T) {
	s := ParseShortstat(" 3 files changed, 42 insertions(+), 7 deletions(-)")
	if s.Files != 3 || s.Added != 42 || s.Deleted != 7 {
		t.Fatalf("%+v", s)
	}
	s = ParseShortstat(" 1 file changed, 1 insertion(+)")
	if s.Files != 1 || s.Added != 1 || s.Deleted != 0 {
		t.Fatalf("%+v", s)
	}
	if s := ParseShortstat(""); s.Files != 0 || !s.OK {
		t.Fatalf("%+v", s)
	}
}

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestWorktreeFlow(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "api")
	os.MkdirAll(repo, 0o755)
	run(t, repo, "init", "-q", "-b", "main")
	if _, err := Add(repo, filepath.Join(root, "wt"), "herd/", "x"); err == nil {
		t.Fatal("empty repo should fail")
	}
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\n"), 0o644)
	run(t, repo, "add", ".")
	run(t, repo, "commit", "-q", "-m", "init")

	if Toplevel(filepath.Join(repo)) == "" || Toplevel(root) != "" {
		t.Fatal("toplevel")
	}
	wt, err := Add(repo, filepath.Join(root, "wt"), "herd/", "fix-auth")
	if err != nil {
		t.Fatal(err)
	}
	if wt.Branch != "herd/fix-auth" || Branch(wt.Path) != "herd/fix-auth" || wt.Base == "" {
		t.Fatalf("%+v", wt)
	}
	wt2, err := Add(repo, filepath.Join(root, "wt"), "herd/", "fix-auth")
	if err != nil || wt2.Branch != "herd/fix-auth-2" {
		t.Fatalf("second worktree: %+v %v", wt2, err)
	}

	os.WriteFile(filepath.Join(wt.Path, "a.txt"), []byte("one\ntwo\nthree\n"), 0o644)
	st := DiffStat(wt.Path, wt.Base)
	if st.Added != 2 || st.Files != 1 || st.Branch != "herd/fix-auth" {
		t.Fatalf("diffstat %+v", st)
	}
	if !Dirty(wt.Path) {
		t.Fatal("should be dirty")
	}
	if _, err := Remove(repo, wt.Path, wt.Branch, false); err == nil {
		t.Fatal("dirty worktree should not be removed without force")
	}
	// commit so the branch is not merged: branch must be kept
	run(t, wt.Path, "commit", "-q", "-am", "work")
	res, err := Remove(repo, wt.Path, wt.Branch, false)
	if err != nil || res.BranchDeleted || res.BranchNote == "" {
		t.Fatalf("%+v %v", res, err)
	}
	// clean, merged branch is deleted
	res, err = Remove(repo, wt2.Path, wt2.Branch, false)
	if err != nil || !res.BranchDeleted {
		t.Fatalf("%+v %v", res, err)
	}
}
