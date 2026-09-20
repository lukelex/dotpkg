package appimage

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

func TestResolveUsesGitHubAssetDigest(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/repos/acme/tool/releases/tags/v1.2.3" {
			t.Fatalf("request path = %s", request.URL.Path)
		}
		fmt.Fprint(writer, `{"assets":[{"name":"tool.AppImage","digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}`)
	}))
	defer server.Close()
	client := New()
	client.GitHubAPIBase = server.URL
	client.HTTP = server.Client()
	artifact, err := client.Resolve(context.Background(), Spec{
		Name:    "tool",
		Address: "https://github.com/acme/tool/releases/download/v1.2.3/tool.AppImage",
	})
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Algorithm != "sha256" || artifact.Digest != strings.Repeat("a", 64) || artifact.Version != "v1.2.3" {
		t.Fatalf("artifact = %#v", artifact)
	}
}

func TestResolveUsesChecksumSidecar(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/tool.AppImage.sha256" {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		fmt.Fprintln(writer, strings.Repeat("b", 64)+"  tool.AppImage")
	}))
	defer server.Close()
	client := New()
	client.HTTP = server.Client()
	address := server.URL + "/tool.AppImage"
	artifact, err := client.Resolve(context.Background(), Spec{Name: "tool", Address: address})
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Algorithm != "sha256" || artifact.Digest != strings.Repeat("b", 64) {
		t.Fatalf("artifact = %#v", artifact)
	}
}

func TestResolveRejectsLatest(t *testing.T) {
	_, err := New().Resolve(context.Background(), Spec{
		Name:    "tool",
		Address: "https://example.invalid/releases/latest/tool.AppImage",
	})
	if err == nil || !strings.Contains(err.Error(), "pin a release") {
		t.Fatalf("error = %v", err)
	}
}

func TestInstallVerifiesAndRemovesAppImage(t *testing.T) {
	contents := []byte("fake AppImage")
	digest := sha256.Sum256(contents)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Write(contents)
	}))
	defer server.Close()
	home := t.TempDir()
	target := filepath.Join(home, ".local", "bin", "tool")
	client := New()
	client.HomeDir = home
	client.HTTP = server.Client()
	record, err := client.Install(context.Background(), Spec{Name: "tool", Address: server.URL, Target: target}, Artifact{
		Address:   server.URL,
		Algorithm: "sha256",
		Digest:    hex.EncodeToString(digest[:]),
	})
	if err != nil {
		t.Fatal(err)
	}
	if record.Target != target {
		t.Fatalf("record = %#v", record)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("target is not executable: %v", info.Mode())
	}
	present, err := client.Installed(context.Background(), record)
	if err != nil || !present {
		t.Fatalf("installed = %v, error = %v", present, err)
	}
	if err := client.Remove(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("target stat error = %v", err)
	}
}
