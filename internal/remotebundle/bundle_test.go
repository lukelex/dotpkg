package remotebundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lukelex/dotpkg/internal/manifest"
	"github.com/lukelex/dotpkg/internal/state"
)

const first = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const second = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
const repository = "mattpocock/skills"

func spec(pin string) manifest.RemoteBundle {
	return manifest.RemoteBundle{Name: "grill-me", Repository: repository, Paths: []string{"skills/grill-me", "skills/grilling", "LICENSE"}, Target: "$HOME/.local/share/dotpkg/opencode-skills", Commit: pin}
}

func archive(sha string, invalid string) []byte {
	var b bytes.Buffer
	g := gzip.NewWriter(&b)
	w := tar.NewWriter(g)
	// Repository-wide links outside the requested paths must not block installs.
	_ = w.WriteHeader(&tar.Header{Name: "skills-" + sha + "/AGENTS.md", Typeflag: tar.TypeSymlink, Linkname: "CLAUDE.md"})
	_ = w.WriteHeader(&tar.Header{Name: "skills-" + sha + "/other.md", Typeflag: tar.TypeLink, Linkname: "skills-" + sha + "/LICENSE"})
	license := []byte("MIT license for test fixture")
	_ = w.WriteHeader(&tar.Header{Name: "skills-" + sha + "/LICENSE", Mode: 0644, Size: int64(len(license))})
	_, _ = w.Write(license)
	for _, skill := range []string{"grill-me", "grilling"} {
		if invalid == "missing" && skill == "grilling" {
			continue
		}
		body := []byte("---\nname: " + skill + "\n---\n" + sha)
		name := "skills-" + sha + "/skills/" + skill + "/SKILL.md"
		if invalid == "path" {
			name = "../" + name
		}
		if invalid == "symlink" || invalid == "hardlink" {
			kind := byte(tar.TypeSymlink)
			if invalid == "hardlink" {
				kind = tar.TypeLink
			}
			_ = w.WriteHeader(&tar.Header{Name: name, Typeflag: kind, Linkname: "/etc/passwd"})
			continue
		}
		_ = w.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: int64(len(body))})
		_, _ = w.Write(body)
	}
	_ = w.Close()
	_ = g.Close()
	return b.Bytes()
}

func TestRevisionsRollbackAndOwnership(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	current := first
	broken := ""
	offline := false
	apiCalls := 0
	downloads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if offline {
			http.Error(w, "offline", http.StatusServiceUnavailable)
			return
		}
		switch {
		case r.URL.Path == "/repos/"+repository:
			apiCalls++
			fmt.Fprint(w, `{"default_branch":"main"}`)
		case r.URL.Path == "/repos/"+repository+"/commits/main":
			apiCalls++
			fmt.Fprintf(w, `{"sha":%q}`, current)
		case strings.HasPrefix(r.URL.Path, "/"+repository+"/tar.gz/"):
			downloads++
			w.Write(archive(filepath.Base(r.URL.Path), broken))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	sys := &System{client: server.Client(), api: server.URL, archive: server.URL}
	ctx := context.Background()
	tracked := map[string]state.RemoteBundle{}
	p, err := sys.Build(ctx, []manifest.RemoteBundle{spec("")}, tracked)
	if err != nil || p.Count() != 1 || p.Changes[0].New != first {
		t.Fatalf("initial plan: %+v, %v", p, err)
	}
	targetPath := filepath.Join(home, ".local/share/dotpkg/opencode-skills")
	if _, err := os.Stat(targetPath); !os.IsNotExist(err) {
		t.Fatal("plan changed filesystem")
	}
	install := func(plan Plan, saveErr error) error {
		sha := plan.Changes[0].New
		return sys.Install(ctx, plan.Specs["grill-me"], tracked["grill-me"], func() error {
			if saveErr != nil {
				return saveErr
			}
			tracked["grill-me"] = state.RemoteBundle{Repository: repository, Target: targetPath, Commit: sha, Paths: spec("").Paths}
			return nil
		})
	}
	if err := install(p, nil); err != nil {
		t.Fatal(err)
	}
	p, err = sys.Build(ctx, []manifest.RemoteBundle{spec("")}, tracked)
	if err != nil || p.Count() != 0 || downloads != 1 || apiCalls != 4 {
		t.Fatalf("noop plan: %+v %v calls=%d downloads=%d", p, err, apiCalls, downloads)
	}
	current = second
	broken = "missing"
	p, err = sys.Build(ctx, []manifest.RemoteBundle{spec("")}, tracked)
	if err != nil || p.Changes[0].Old != first || p.Changes[0].New != second {
		t.Fatalf("update: %+v %v", p, err)
	}
	if err := install(p, nil); err == nil {
		t.Fatal("accepted incomplete bundle")
	}
	if _, valid, _ := installed(targetPath, repository, first, spec("").Paths); !valid {
		t.Fatal("download failure removed old pair")
	}
	for _, kind := range []string{"path", "symlink", "hardlink"} {
		broken = kind
		if err := install(p, nil); err == nil {
			t.Fatalf("accepted %s", kind)
		}
		if _, valid, _ := installed(targetPath, repository, first, spec("").Paths); !valid {
			t.Fatalf("%s damaged old pair", kind)
		}
	}
	broken = ""
	if err := install(p, fmt.Errorf("state write failed")); err == nil {
		t.Fatal("accepted state failure")
	}
	if _, valid, _ := installed(targetPath, repository, first, spec("").Paths); !valid {
		t.Fatal("state failure removed old pair")
	}
	if err := install(p, nil); err != nil {
		t.Fatal(err)
	}
	if _, valid, _ := installed(targetPath, repository, second, spec("").Paths); !valid {
		t.Fatal("pair not updated")
	}
	before := apiCalls
	p, err = sys.Build(ctx, []manifest.RemoteBundle{spec(first)}, tracked)
	if err != nil || apiCalls != before || p.Count() != 1 || p.Changes[0].New != first {
		t.Fatalf("pin: %+v %v", p, err)
	}
	offline = true
	if _, err := sys.Build(ctx, []manifest.RemoteBundle{spec("")}, tracked); err == nil {
		t.Fatal("offline upstream reported no-op")
	}
	if _, err := sys.Build(ctx, []manifest.RemoteBundle{spec(first)}, tracked); err != nil {
		t.Fatalf("pin queried upstream: %v", err)
	}
	clean := CleanPlan(nil, tracked)
	if clean.Count() != 1 {
		t.Fatal(clean)
	}
	if err := Remove("grill-me", tracked["grill-me"]); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(targetPath); !os.IsNotExist(err) {
		t.Fatal("clean left bundle")
	}
}

func TestConflictAndModifiedClean(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	targetPath := filepath.Join(home, ".local/share/dotpkg/opencode-skills")
	if err := os.MkdirAll(targetPath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(targetPath, "user.txt"), []byte("owned"), 0644); err != nil {
		t.Fatal(err)
	}
	sys := &System{client: http.DefaultClient}
	if _, err := sys.Build(context.Background(), []manifest.RemoteBundle{spec(first)}, nil); err == nil {
		t.Fatal("accepted user directory")
	}
	record := state.RemoteBundle{Repository: repository, Target: targetPath, Commit: first, Paths: spec("").Paths}
	if err := Remove("grill-me", record); err == nil {
		t.Fatal("removed unowned directory")
	}
	if _, err := os.Stat(filepath.Join(targetPath, "user.txt")); err != nil {
		t.Fatal(err)
	}
}

func TestCleanPreservesAddedUserFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	current := first
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive(current, ""))
	}))
	defer server.Close()
	sys := &System{client: server.Client(), archive: server.URL}
	declaration := spec(first)
	targetPath, err := target(declaration)
	if err != nil {
		t.Fatal(err)
	}
	if err := sys.Install(context.Background(), declaration, state.RemoteBundle{}, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	userFile := filepath.Join(targetPath, "user.txt")
	if err := os.WriteFile(userFile, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	record := state.RemoteBundle{Repository: repository, Target: targetPath, Commit: first, Paths: spec("").Paths}
	if err := Remove("grill-me", record); err == nil {
		t.Fatal("clean removed user data")
	}
	if _, err := os.Stat(userFile); err != nil {
		t.Fatal(err)
	}
}

func TestGenericRequestedFilesAndChangedPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for name, body := range map[string]string{"repo-" + first + "/docs/guide.txt": "guide", "repo-" + first + "/config/settings.json": "{}", "repo-" + first + "/ignored.txt": "not installed"} {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: int64(len(body))})
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	_ = gz.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(b.Bytes()) }))
	defer server.Close()
	sys := &System{client: server.Client(), archive: server.URL}
	declaration := manifest.RemoteBundle{Name: "docs", Repository: "example/project", Paths: []string{"docs/guide.txt", "config/settings.json"}, Target: "$HOME/.local/share/dotpkg/docs", Commit: first}
	targetPath, err := target(declaration)
	if err != nil {
		t.Fatal(err)
	}
	tracked := map[string]state.RemoteBundle{}
	p, err := sys.Build(context.Background(), []manifest.RemoteBundle{declaration}, tracked)
	if err != nil {
		t.Fatal(err)
	}
	if err := sys.Install(context.Background(), p.Specs["docs"], state.RemoteBundle{}, func() error {
		tracked["docs"] = state.RemoteBundle{Repository: declaration.Repository, Target: targetPath, Commit: first, Paths: declaration.Paths}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"guide.txt", "settings.json"} {
		if _, err := os.Stat(filepath.Join(targetPath, file)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(targetPath, "ignored.txt")); !os.IsNotExist(err) {
		t.Fatal("installed unrequested file")
	}
	if err := os.Mkdir(filepath.Join(targetPath, "user-empty-dir"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := Remove("docs", tracked["docs"]); err == nil {
		t.Fatal("removed added empty directory")
	}
	if err := os.Remove(filepath.Join(targetPath, "user-empty-dir")); err != nil {
		t.Fatal(err)
	}
	changed := declaration
	changed.Paths = []string{"docs/guide.txt"}
	p, err = sys.Build(context.Background(), []manifest.RemoteBundle{changed}, tracked)
	if err != nil || p.Count() != 1 {
		t.Fatalf("changed path plan: %+v %v", p, err)
	}
	if err := Remove("docs", tracked["docs"]); err != nil {
		t.Fatal(err)
	}
}
