// Package selfupdate replaces the running herd binary with a newer one,
// either a release from GitHub or a build of a git ref with go install.
package selfupdate

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	Repo   = "phankieuphu/herd"
	Module = "github.com/" + Repo + "/cmd/herd"
)

// Base URLs, changed by tests.
var (
	APIBase      = "https://api.github.com"
	DownloadBase = "https://github.com"
)

var client = &http.Client{Timeout: 2 * time.Minute}

// Latest returns the tag of the newest release, like "v0.2.0".
func Latest(ctx context.Context) (string, error) {
	body, err := get(ctx, APIBase+"/repos/"+Repo+"/releases/latest")
	if err != nil {
		return "", err
	}
	var r struct {
		Tag string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &r); err != nil || r.Tag == "" {
		return "", fmt.Errorf("cannot read the latest release: %v", err)
	}
	return r.Tag, nil
}

// Download fetches the release archive for goos/goarch, checks it against
// the release's checksums.txt and returns the herd binary inside.
func Download(ctx context.Context, tag, goos, goarch string) ([]byte, error) {
	name := fmt.Sprintf("herd_%s_%s_%s.tar.gz", strings.TrimPrefix(tag, "v"), goos, goarch)
	base := DownloadBase + "/" + Repo + "/releases/download/" + tag + "/"
	sums, err := get(ctx, base+"checksums.txt")
	if err != nil {
		return nil, err
	}
	want := ""
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) == 2 && f[1] == name {
			want = f[0]
		}
	}
	if want == "" {
		return nil, fmt.Errorf("release %s has no build for %s/%s", tag, goos, goarch)
	}
	archive, err := get(ctx, base+name)
	if err != nil {
		return nil, err
	}
	if sum := sha256.Sum256(archive); hex.EncodeToString(sum[:]) != want {
		return nil, fmt.Errorf("%s: checksum does not match", name)
	}
	return extract(archive)
}

func extract(archive []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, errors.New("no herd binary in the archive")
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg && filepath.Base(h.Name) == "herd" {
			return io.ReadAll(io.LimitReader(tr, 200<<20))
		}
	}
}

// Replace swaps the file at path for bin. The new file is written next to
// it and renamed over it, so running copies keep working.
func Replace(path string, bin []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".herd-update-*")
	if err != nil {
		return writeErr(path, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return writeErr(path, err)
	}
	return nil
}

// GoInstall builds ref (a tag, branch or commit) with go install into dir.
// GOPROXY=direct fetches from GitHub, so a just-merged commit is found.
func GoInstall(ref, dir string) error {
	gobin, err := exec.LookPath("go")
	if err != nil {
		return errors.New("go is not installed; it is needed to build a branch or commit")
	}
	cmd := exec.Command(gobin, "install", Module+"@"+ref)
	cmd.Env = append(os.Environ(), "GOBIN="+dir, "GOPROXY=direct")
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go install %s@%s: %w", Module, ref, err)
	}
	return nil
}

// Same reports whether two versions name the same release ("v0.1.1" and
// "0.1.1" are the same).
func Same(a, b string) bool {
	return strings.TrimPrefix(a, "v") == strings.TrimPrefix(b, "v")
}

func writeErr(path string, err error) error {
	if errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("cannot write %s (permission denied); run the update with sudo or move herd to a folder you own", path)
	}
	return err
}

func get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, res.Status)
	}
	return io.ReadAll(io.LimitReader(res.Body, 200<<20))
}
