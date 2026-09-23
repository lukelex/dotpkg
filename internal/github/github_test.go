package github

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

func TestInstallUpdateAndRemovePinnedSourceArchive(t *testing.T) {
	archive := tarGzip(t, map[string]tarFile{
		"hunk-deadbeef/bin/hunk-pager":             {contents: []byte("first"), mode: 0o755},
		"hunk-deadbeef/share/hunk/delta.gitconfig": {contents: []byte("[delta]"), mode: 0o644},
	})
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/lukelex/hunk/archive/deadbeef.tar.gz" {
			t.Fatalf("request path = %s", request.URL.Path)
		}
		_, _ = writer.Write(archive)
	}))
	defer server.Close()
	home := t.TempDir()
	client := New()
	client.HomeDir = home
	client.GitHubBase = server.URL
	client.HTTP = server.Client()
	spec := Spec{Name: "hunk", Repo: "lukelex/hunk", Ref: "deadbeef", Archive: "source", Sha256: digest(archive), Files: []File{
		{Source: "bin/hunk-pager", Target: "$HOME/.local/bin/hunk-pager"},
		{Source: "share/hunk/delta.gitconfig", Target: "$HOME/.local/share/hunk/delta.gitconfig"},
	}}
	artifact, err := client.Resolve(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	record, err := client.Install(context.Background(), spec, artifact, nil)
	if err != nil {
		t.Fatal(err)
	}
	if present, err := client.Installed(context.Background(), record); err != nil || !present {
		t.Fatalf("installed = %v, error = %v", present, err)
	}
	info, err := os.Stat(filepath.Join(home, ".local", "bin", "hunk-pager"))
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("installed executable = %v, error = %v", info, err)
	}
	if err := client.Remove(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "bin", "hunk-pager")); !os.IsNotExist(err) {
		t.Fatalf("removed file stat error = %v", err)
	}
}

func TestInstallRejectsChecksumTraversalAndSymlinks(t *testing.T) {
	archive := tarGzip(t, map[string]tarFile{"root/bin/tool": {contents: []byte("tool"), mode: 0o755}})
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) { _, _ = writer.Write(archive) }))
	defer server.Close()
	client := New()
	client.HomeDir = t.TempDir()
	client.HTTP = server.Client()
	spec := Spec{Name: "tool", Repo: "acme/tool", Ref: "0123456789abcdef", Archive: "source", Sha256: strings.Repeat("a", 64), Files: []File{{Source: "bin/tool", Target: "$HOME/.local/bin/tool"}}}
	_, err := client.Install(context.Background(), spec, Artifact{Address: server.URL, Sha256: spec.Sha256}, nil)
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("checksum error = %v", err)
	}
	traversal := tarGzip(t, map[string]tarFile{"../outside": {contents: []byte("bad"), mode: 0o644}})
	_, err = readArchive(writeArchive(t, traversal), spec.Files)
	if err == nil || !strings.Contains(err.Error(), "traversal") {
		t.Fatalf("traversal error = %v", err)
	}
	symlink := tarGzip(t, map[string]tarFile{"root/bin/tool": {link: "../../outside"}})
	_, err = readArchive(writeArchive(t, symlink), spec.Files)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink error = %v", err)
	}
}

func TestInstallRefusesUnmanagedAndModifiedTargets(t *testing.T) {
	archive := tarGzip(t, map[string]tarFile{"root/bin/tool": {contents: []byte("one"), mode: 0o755}})
	home := t.TempDir()
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) { _, _ = writer.Write(archive) }))
	defer server.Close()
	client := New()
	client.HomeDir, client.HTTP = home, server.Client()
	spec := Spec{Name: "tool", Repo: "acme/tool", Ref: "abc123", Archive: "source", Sha256: digest(archive), Files: []File{{Source: "bin/tool", Target: "$HOME/.local/bin/tool"}}}
	if err := os.MkdirAll(filepath.Join(home, ".local", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".local", "bin", "tool"), []byte("user"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := client.Install(context.Background(), spec, Artifact{Address: server.URL, Sha256: spec.Sha256}, nil)
	if err == nil || !strings.Contains(err.Error(), "unmanaged") {
		t.Fatalf("unmanaged overwrite error = %v", err)
	}
	if err := os.Remove(filepath.Join(home, ".local", "bin", "tool")); err != nil {
		t.Fatal(err)
	}
	record, err := client.Install(context.Background(), spec, Artifact{Address: server.URL, Sha256: spec.Sha256}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".local", "bin", "tool"), []byte("changed"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = client.Install(context.Background(), spec, Artifact{Address: server.URL, Sha256: spec.Sha256}, &record)
	if err == nil || !strings.Contains(err.Error(), "modified") {
		t.Fatalf("modified update error = %v", err)
	}
}

func TestResolveReleaseAssetWithoutGitHubAPI(t *testing.T) {
	client := New()
	client.GitHubBase = "https://github.example"
	artifact, err := client.Resolve(context.Background(), Spec{
		Name: "tool", Repo: "acme/tool", Ref: "v1.2.3", Archive: "tool.tar.gz", Sha256: strings.Repeat("a", 64),
		Files: []File{{Source: "bin/tool", Target: "$HOME/.local/bin/tool"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://github.example/acme/tool/releases/download/v1.2.3/tool.tar.gz"; artifact.Address != want {
		t.Fatalf("release asset URL = %q, want %q", artifact.Address, want)
	}
}

type tarFile struct {
	contents []byte
	mode     int64
	link     string
}

func tarGzip(t *testing.T, files map[string]tarFile) []byte {
	t.Helper()
	var result bytes.Buffer
	writer := gzip.NewWriter(&result)
	tarWriter := tar.NewWriter(writer)
	for name, file := range files {
		header := &tar.Header{Name: name, Mode: file.mode, Size: int64(len(file.contents)), Typeflag: tar.TypeReg}
		if file.link != "" {
			header.Typeflag, header.Linkname, header.Size = tar.TypeSymlink, file.link, 0
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if len(file.contents) > 0 {
			if _, err := tarWriter.Write(file.contents); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return result.Bytes()
}

func digest(contents []byte) string {
	value := sha256.Sum256(contents)
	return hex.EncodeToString(value[:])
}

func writeArchive(t *testing.T, contents []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "archive.tar.gz")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
