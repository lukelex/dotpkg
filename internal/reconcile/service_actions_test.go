package reconcile

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lukelex/dotpkg/internal/recovery"
	"github.com/lukelex/dotpkg/internal/resource"
	"github.com/lukelex/dotpkg/internal/state"
)

type failingServiceSystem struct {
	resource.System
	attempts int
	fail     bool
}

func (*failingServiceSystem) CurrentGroups(context.Context, string) ([]string, error) {
	return nil, nil
}

func (*failingServiceSystem) ServiceExists(context.Context, bool, string) (bool, error) {
	return true, nil
}

func (*failingServiceSystem) ServiceEnabled(context.Context, bool, string) (bool, error) {
	return true, nil
}

func (*failingServiceSystem) ServiceActive(context.Context, bool, string) (bool, error) {
	return true, nil
}

func (f *failingServiceSystem) RestartService(context.Context, bool, string) error {
	f.attempts++
	if f.fail {
		return errors.New("service failed")
	}
	return nil
}

func TestServiceFailureDoesNotRollbackSuccessfulPackages(t *testing.T) {
	testServiceFailureDoesNotRollbackSuccessfulPackages(t, true)
}

func TestServiceFailureWithoutResourcesDoesNotRollbackPackages(t *testing.T) {
	testServiceFailureDoesNotRollbackSuccessfulPackages(t, false)
}

func testServiceFailureDoesNotRollbackSuccessfulPackages(t *testing.T, resources bool) {
	t.Helper()
	root := t.TempDir()
	manifestPath := filepath.Join(root, "packages.yaml")
	statePath := filepath.Join(root, "state.yaml")
	contents := `source: repo
common:
  packages:
    headless:
      tool:
        services:
          user: [tool.service]
profiles:
  server:
    packages:
      headless: {}
`
	if err := os.WriteFile(manifestPath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	s := state.New(statePath)
	s.SetItems([]string{"user:tool.service"}, state.ManagedKey, state.ManagedServices)
	if err := s.Write(); err != nil {
		t.Fatal(err)
	}
	services := &failingServiceSystem{fail: true}
	packages := &fakeBackend{installed: map[string]bool{}}
	o := Options{ManifestPath: manifestPath, StatePath: statePath, RootPath: root, Profile: "server", Resources: resources, Yes: true, ResourceSystem: services, Input: strings.NewReader(""), Output: io.Discard}
	err := Sync(context.Background(), o, packages)
	var actionErr *resource.ServiceActionError
	if !errors.As(err, &actionErr) {
		t.Fatalf("error=%v", err)
	}
	if len(packages.install) != 1 || len(packages.remove) != 0 || services.attempts != 1 {
		t.Fatalf("install=%v remove=%v attempts=%d", packages.install, packages.remove, services.attempts)
	}
	if _, err := recovery.Load(statePath); !os.IsNotExist(err) {
		t.Fatalf("journal remains: %v", err)
	}
	loaded, err := state.Load(statePath)
	if err != nil {
		t.Fatal(err)
	}
	tracking, err := loaded.ServiceTracking()
	if err != nil {
		t.Fatal(err)
	}
	if len(tracking["user:tool.service"].Packages) != 1 {
		t.Fatal("package action was lost")
	}
	services.fail = false
	packages.installed["tool"] = true
	if err := Sync(context.Background(), o, packages); err != nil {
		t.Fatal(err)
	}
	if services.attempts != 2 {
		t.Fatal("pending action was not retried")
	}
}

func TestServiceActionPlanIncludesReasons(t *testing.T) {
	doc := NewPlanDocument(Plan{}, &resource.Plan{ServiceActions: []resource.ServiceAction{{Service: "user:tool.service", Action: "reload", Reasons: []string{"/config"}}}}, Options{})
	last := doc.Stages[len(doc.Stages)-1]
	if doc.Changes != 1 || last.Name != "service_actions" || len(last.Changes[0].Reasons) != 1 {
		t.Fatalf("plan=%+v", doc)
	}
}
