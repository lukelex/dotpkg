package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitHubReleaseUpdate(t *testing.T) {
	asset, err := assetName()
	if err != nil {
		t.Skip(err)
	}
	path := filepath.Join(t.TempDir(), "dotpkg")
	if err := os.WriteFile(path, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	binary := []byte("new executable")
	digest := sha256.Sum256(binary)
	sums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(digest[:]), asset)
	requests := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests[r.URL.Path]++
		switch r.URL.Path {
		case "/repos/lukelex/dotpkg/releases/latest":
			fmt.Fprint(w, `{"tag_name":"v0.17.0"}`)
		case "/lukelex/dotpkg/releases/download/v0.17.0/SHA256SUMS":
			fmt.Fprint(w, sums)
		case "/lukelex/dotpkg/releases/download/v0.17.0/" + asset:
			_, _ = w.Write(binary)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := &Client{HTTP: server.Client(), API: server.URL, Downloads: server.URL}
	latest, newer, err := client.Check(context.Background(), path, "v0.16.0")
	if err != nil || latest != "v0.17.0" || !newer {
		t.Fatalf("check = %s, %t, %v", latest, newer, err)
	}
	if len(requests) != 1 {
		t.Fatalf("dry-run downloaded assets: %v", requests)
	}
	if err := client.Install(context.Background(), path, latest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(binary) {
		t.Fatalf("binary = %q, %v", got, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v, %v", info, err)
	}
	if err := os.WriteFile(path, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	sums = strings.Repeat("0", 64) + "  " + asset + "\n"
	if err := client.Install(context.Background(), path, latest); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("bad checksum accepted: %v", err)
	}
	got, err = os.ReadFile(path)
	if err != nil || string(got) != "old" {
		t.Fatalf("corrupted executable = %q, %v", got, err)
	}
}

func TestVersionPartsAndChecksum(t *testing.T) {
	for _, value := range []string{"latest", "v0.1", "v0.1.0-rc1", "v0.01.0", "v0.1.0/../../x"} {
		if _, err := versionParts(value); err == nil {
			t.Errorf("accepted tag %q", value)
		}
	}
	for _, contents := range []string{"", strings.Repeat("a", 64) + "  wrong\n", strings.Repeat("a", 64) + "  dotpkg-linux-amd64\n" + strings.Repeat("a", 64) + "  dotpkg-linux-amd64\n"} {
		if _, err := checksum([]byte(contents), "dotpkg-linux-amd64"); err == nil {
			t.Errorf("accepted checksums %q", contents)
		}
	}
}

func TestRejectPackageManagedExecutable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dotpkg")
	if err := os.WriteFile(path, []byte("stand-in"), 0o755); err != nil {
		t.Fatal(err)
	}
	manager := filepath.Join(dir, "pacman")
	if err := os.WriteFile(manager, []byte("#!/bin/sh\necho dotpkg-bin\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	client := New()
	if _, _, err := client.Check(context.Background(), path, "v0.16.0"); err == nil || !strings.Contains(err.Error(), "package-managed") {
		t.Fatalf("package-managed binary accepted: %v", err)
	}
}
