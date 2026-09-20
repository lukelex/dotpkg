package backend

import (
	"context"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/lukelex/dotpkg/internal/resource"
)

func TestNewDefaultsToArchBackend(t *testing.T) {
	t.Setenv("DOTPKG_BACKEND", "")
	value, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := value.(*Arch); !ok {
		t.Fatalf("backend type = %T, want *Arch", value)
	}
}

func TestNewRejectsUnsupportedBackend(t *testing.T) {
	t.Setenv("DOTPKG_BACKEND", "opensuse")
	if _, err := New(); err == nil {
		t.Fatal("unsupported backend was accepted")
	}
}

func TestArchConfigureAppliesOperationalOptions(t *testing.T) {
	arch := NewArch()
	if err := arch.Configure(Options{
		HTTPTimeout:   2 * time.Second,
		AURRetries:    5,
		AURRetryDelay: 20 * time.Millisecond,
		Logger:        io.Discard,
	}); err != nil {
		t.Fatal(err)
	}
	if arch.HTTPClient.Timeout != 2*time.Second || arch.AURRetries != 5 || arch.AURRetryDelay != 20*time.Millisecond {
		t.Fatalf("configuration = %#v", arch)
	}
}

func TestDebianUsesAptCommands(t *testing.T) {
	runner := &fakeRunner{outputs: map[string]fakeOutput{
		"dpkg-query": {value: []byte("install ok installed")},
		"apt-cache":  {value: []byte("git\ncurl\n")},
	}}
	debian := NewDebian()
	debian.Runner = runner
	installed, err := debian.IsInstalled(context.Background(), "git")
	if err != nil || !installed {
		t.Fatalf("installed = %v, error = %v", installed, err)
	}
	packages, err := debian.RepositoryPackages(context.Background())
	if err != nil || len(packages) != 2 {
		t.Fatalf("packages = %#v, error = %v", packages, err)
	}
	if err := debian.Install(context.Background(), []string{"git"}, nil, "server"); err != nil {
		t.Fatal(err)
	}
	if err := debian.Remove(context.Background(), []string{"git"}, "server"); err != nil {
		t.Fatal(err)
	}
	var runs [][]string
	for _, call := range runner.calls {
		if call.kind == "run" {
			runs = append(runs, append([]string{call.name}, call.args...))
		}
	}
	want := [][]string{
		{"sudo", "apt-get", "install", "-y", "git"},
		{"sudo", "apt-get", "purge", "-y", "git"},
	}
	if !reflect.DeepEqual(runs, want) {
		t.Fatalf("runs = %#v, want %#v", runs, want)
	}
}

func TestFedoraUsesDnfAndSupportsResources(t *testing.T) {
	var _ resource.System = (*Fedora)(nil)
	runner := &fakeRunner{outputs: map[string]fakeOutput{
		"rpm": {value: []byte("installed")},
		"dnf": {value: []byte("git\n")},
	}}
	fedora := NewFedora()
	fedora.Runner = runner
	installed, err := fedora.IsInstalled(context.Background(), "git")
	if err != nil || !installed {
		t.Fatalf("installed = %v, error = %v", installed, err)
	}
	packages, err := fedora.RepositoryPackages(context.Background())
	if err != nil || len(packages) != 1 {
		t.Fatalf("packages = %#v, error = %v", packages, err)
	}
	if err := fedora.Install(context.Background(), []string{"git"}, nil, "server"); err != nil {
		t.Fatal(err)
	}
}
