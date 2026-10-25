package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tarGz(t *testing.T, name string, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range []struct {
		name string
		data []byte
	}{{"README.md", []byte("readme")}, {name, data}} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o755, Size: int64(len(f.data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write(f.data)
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func fakeGitHub(t *testing.T, archive []byte, sum string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/" + Repo + "/releases/latest":
			_, _ = w.Write([]byte(`{"tag_name":"v0.2.0"}`))
		case "/" + Repo + "/releases/download/v0.2.0/checksums.txt":
			_, _ = w.Write([]byte(sum + "  herd_0.2.0_darwin_arm64.tar.gz\nabc  herd_0.2.0_linux_amd64.tar.gz\n"))
		case "/" + Repo + "/releases/download/v0.2.0/herd_0.2.0_darwin_arm64.tar.gz":
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	oldAPI, oldDL := APIBase, DownloadBase
	APIBase, DownloadBase = srv.URL, srv.URL
	t.Cleanup(func() { APIBase, DownloadBase = oldAPI, oldDL })
}

func TestDownloadAndReplace(t *testing.T) {
	archive := tarGz(t, "herd", []byte("new binary"))
	s := sha256.Sum256(archive)
	fakeGitHub(t, archive, hex.EncodeToString(s[:]))
	ctx := context.Background()

	tag, err := Latest(ctx)
	if err != nil || tag != "v0.2.0" {
		t.Fatalf("latest %q %v", tag, err)
	}
	bin, err := Download(ctx, tag, "darwin", "arm64")
	if err != nil || string(bin) != "new binary" {
		t.Fatalf("download %q %v", bin, err)
	}
	if _, err := Download(ctx, tag, "windows", "amd64"); err == nil || !strings.Contains(err.Error(), "no build") {
		t.Fatalf("missing platform: %v", err)
	}

	path := filepath.Join(t.TempDir(), "herd")
	if err := os.WriteFile(path, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Replace(path, bin); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	st, _ := os.Stat(path)
	if string(got) != "new binary" || st.Mode().Perm() != 0o755 {
		t.Fatalf("replaced %q %v", got, st.Mode())
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".herd-update-*")); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
}

func TestBadChecksum(t *testing.T) {
	fakeGitHub(t, tarGz(t, "herd", []byte("evil")), strings.Repeat("0", 64))
	if _, err := Download(context.Background(), "v0.2.0", "darwin", "arm64"); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("want checksum error, got %v", err)
	}
}

func TestSame(t *testing.T) {
	if !Same("v0.1.1", "0.1.1") || Same("0.1.1", "v0.2.0") {
		t.Fatal("Same")
	}
}
