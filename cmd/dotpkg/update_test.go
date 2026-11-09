package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type updateFakeBackend struct {
	addFakeBackend
	verified, upgraded string
	dry, newer         bool
	verifyErr          error
}

func (f *updateFakeBackend) VerifyExecutable(_ context.Context, path, pkg string) error {
	f.verified = path + ":" + pkg
	return f.verifyErr
}
func (f *updateFakeBackend) AvailableUpdate(_ context.Context, pkg, origin string, dry bool) (string, string, bool, error) {
	f.dry = dry
	return "1", "2", f.newer, nil
}
func (f *updateFakeBackend) UpgradePackage(_ context.Context, pkg, origin string) error {
	f.upgraded = pkg + ":" + origin
	return nil
}

func TestUpdateHostOptInAndDryRun(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "packages.yaml")
	hostDir := filepath.Join(dir, "hosts")
	if err := os.Mkdir(hostDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(base, []byte("source: repo\nself_update:\n  package: untrusted\n  origin: repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	host := filepath.Join(hostDir, "testhost.yaml")
	if err := os.WriteFile(host, []byte("common: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	previousHostname, previousExecutable, previousInput := updateHostname, updateExecutable, updateInput
	t.Cleanup(func() {
		updateHostname, updateExecutable, updateInput = previousHostname, previousExecutable, previousInput
	})
	updateHostname = func() (string, error) { return "testhost", nil }
	updateExecutable = func() (string, error) { return base, nil }
	fake := &updateFakeBackend{newer: true}
	args := []string{"--manifest", base, "--dry-run"}
	if err := updateCommand(context.Background(), args, fake); err != nil {
		t.Fatalf("base opt-in should work when overlay omits it: %v", err)
	}
	if fake.verified != base+":untrusted" || !fake.dry || fake.upgraded != "" {
		t.Fatalf("unexpected base update activity: %+v", fake)
	}
	if err := os.WriteFile(host, []byte("self_update:\n  package: dotpkg-bin\n  origin: aur\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := updateCommand(context.Background(), args, fake); err != nil {
		t.Fatal(err)
	}
	if fake.verified != base+":dotpkg-bin" || !fake.dry || fake.upgraded != "" {
		t.Fatalf("unexpected update activity: %+v", fake)
	}
	updateInput = strings.NewReader("\n")
	if err := updateCommand(context.Background(), []string{"--manifest", base}, fake); err != nil {
		t.Fatal(err)
	}
	if fake.upgraded != "" {
		t.Fatal("default-No confirmation upgraded package")
	}
	if err := updateCommand(context.Background(), []string{"--manifest", base, "--yes"}, fake); err != nil {
		t.Fatal(err)
	}
	if fake.upgraded != "dotpkg-bin:aur" {
		t.Fatalf("upgraded = %q", fake.upgraded)
	}
}

func TestUpdateWithoutHostOverlay(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "packages.yaml")
	if err := os.WriteFile(base, []byte("source: repo\nself_update: {package: dotpkg-bin, origin: aur}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	previous, previousExecutable := updateHostname, updateExecutable
	t.Cleanup(func() { updateHostname, updateExecutable = previous, previousExecutable })
	updateHostname = func() (string, error) { return "missing", nil }
	updateExecutable = func() (string, error) { return base, nil }
	fake := &updateFakeBackend{newer: true}
	if err := updateCommand(context.Background(), []string{"--manifest", base, "--dry-run"}, fake); err != nil {
		t.Fatal(err)
	}
	if fake.verified != base+":dotpkg-bin" || !fake.dry || fake.upgraded != "" {
		t.Fatalf("unexpected update activity: %+v", fake)
	}
	if err := updateCommand(context.Background(), []string{"--manifest", base, "--host", "typo", "--dry-run"}, fake); err == nil || !strings.Contains(err.Error(), "host overlay") {
		t.Fatalf("explicit missing host accepted: %v", err)
	}
	fake.verifyErr = errors.New("executable is not package-managed")
	if err := updateCommand(context.Background(), []string{"--manifest", base, "--yes"}, fake); !errors.Is(err, fake.verifyErr) || fake.upgraded != "" {
		t.Fatalf("ownership check not enforced: %v, %+v", err, fake)
	}
	if err := os.WriteFile(base, []byte("source: repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := updateCommand(context.Background(), []string{"--manifest", base, "--dry-run"}, fake); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("missing opt-in accepted: %v", err)
	}
}
