// Package gitwt runs the git commands herd needs: worktrees and diff stats.
package gitwt

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(msg)
	}
	return strings.TrimSpace(out.String()), nil
}

// Toplevel returns the repo root containing dir, or "" if dir is not in a repo.
func Toplevel(dir string) string {
	out, err := git(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return ""
	}
	return out
}

// Head returns the commit sha of HEAD, or "" (e.g. empty repo).
func Head(dir string) string {
	out, err := git(dir, "rev-parse", "--verify", "-q", "HEAD")
	if err != nil {
		return ""
	}
	return out
}

// Branch returns the current branch name ("" when detached).
func Branch(dir string) string {
	out, err := git(dir, "symbolic-ref", "--short", "-q", "HEAD")
	if err != nil {
		return ""
	}
	return out
}

// BranchExists reports whether a local branch exists.
func BranchExists(repo, name string) bool {
	_, err := git(repo, "show-ref", "--verify", "-q", "refs/heads/"+name)
	return err == nil
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// Slug makes a short, safe name for branches, folders and sessions.
func Slug(s string) string {
	s = slugRe.ReplaceAllString(strings.ToLower(s), "-")
	s = strings.Trim(s, "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	if s == "" {
		s = "agent"
	}
	return s
}

// Worktree is a created worktree.
type Worktree struct {
	Repo   string
	Path   string
	Branch string
	Base   string
}

// Add creates a worktree for a new branch at HEAD of repo. root is the folder
// that holds all herd worktrees.
func Add(repo, root, prefix, slug string) (Worktree, error) {
	base := Head(repo)
	if base == "" {
		return Worktree{}, fmt.Errorf("%s has no commits yet; cannot create a worktree", repo)
	}
	name := filepath.Base(repo)
	branch := prefix + slug
	path := filepath.Join(root, name, slug)
	for i := 2; BranchExists(repo, branch) || exists(path); i++ {
		branch = fmt.Sprintf("%s%s-%d", prefix, slug, i)
		path = filepath.Join(root, name, fmt.Sprintf("%s-%d", slug, i))
		if i > 99 {
			return Worktree{}, errors.New("too many worktrees with the same name")
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Worktree{}, err
	}
	if _, err := git(repo, "worktree", "add", "-b", branch, path, base); err != nil {
		return Worktree{}, fmt.Errorf("git worktree add: %w", err)
	}
	return Worktree{Repo: repo, Path: path, Branch: branch, Base: base}, nil
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// Dirty reports whether a worktree has uncommitted or untracked changes.
func Dirty(dir string) bool {
	out, err := git(dir, "status", "--porcelain")
	return err == nil && out != ""
}

// RemoveResult says what Remove did.
type RemoveResult struct {
	BranchDeleted bool
	BranchNote    string
}

// Remove deletes a worktree and, when it is merged, its branch.
func Remove(repo, path, branch string, force bool) (RemoveResult, error) {
	var res RemoveResult
	args := []string{"worktree", "remove", path}
	if force {
		args = []string{"worktree", "remove", "--force", path}
	}
	if exists(path) {
		if _, err := git(repo, args...); err != nil {
			return res, err
		}
	} else {
		_, _ = git(repo, "worktree", "prune")
	}
	if branch != "" && BranchExists(repo, branch) {
		if _, err := git(repo, "branch", "-d", branch); err != nil {
			res.BranchNote = "branch " + branch + " kept (not merged)"
		} else {
			res.BranchDeleted = true
		}
	}
	return res, nil
}

// Stat is a diff size.
type Stat struct {
	Files, Added, Deleted int
	Branch                string
	OK                    bool
}

var (
	filesRe = regexp.MustCompile(`(\d+) files? changed`)
	insRe   = regexp.MustCompile(`(\d+) insertions?\(\+\)`)
	delRe   = regexp.MustCompile(`(\d+) deletions?\(-\)`)
)

// ParseShortstat parses `git diff --shortstat` output.
func ParseShortstat(s string) Stat {
	st := Stat{OK: true}
	if m := filesRe.FindStringSubmatch(s); m != nil {
		st.Files, _ = strconv.Atoi(m[1])
	}
	if m := insRe.FindStringSubmatch(s); m != nil {
		st.Added, _ = strconv.Atoi(m[1])
	}
	if m := delRe.FindStringSubmatch(s); m != nil {
		st.Deleted, _ = strconv.Atoi(m[1])
	}
	return st
}

// DiffStat measures changes in dir since base (committed and not committed).
func DiffStat(dir, base string) Stat {
	if base == "" {
		base = "HEAD"
	}
	out, err := git(dir, "diff", "--shortstat", base)
	if err != nil {
		return Stat{}
	}
	st := ParseShortstat(out)
	st.Branch = Branch(dir)
	return st
}

// DiffScript is the shell script shown in the review popup.
func DiffScript(base string) string {
	if base == "" {
		base = "HEAD"
	}
	q := "'" + strings.ReplaceAll(base, "'", `'\''`) + "'"
	return `{ printf '\033[1m%s\033[0m\n' "$(git symbolic-ref --short -q HEAD || git rev-parse --short HEAD)"; ` +
		`git -c color.status=always status --short; echo; ` +
		`git --no-pager diff --stat ` + q + `; echo; ` +
		`git -c color.ui=always diff ` + q + `; } | less -R`
}
