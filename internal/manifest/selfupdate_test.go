package manifest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHostSelfUpdate(t *testing.T) {
	for _, test := range []struct {
		yaml string
		ok   bool
	}{
		{"self_update: {package: dotpkg-bin, origin: aur}\n", true},
		{"common: {}\n", false},
		{"self_update: null\n", false},
		{"self_update: {package: 'dotpkg;unsafe', origin: repo}\n", false},
		{"self_update: {package: dotpkg, origin: appimage}\n", false},
		{"self_update: {package: dotpkg, origin: repo, extra: true}\n", false},
	} {
		path := filepath.Join(t.TempDir(), "host.yaml")
		if err := os.WriteFile(path, []byte(test.yaml), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := SelfUpdateSetting(path, "")
		if (err == nil) != test.ok {
			t.Errorf("%q: setting=%+v err=%v", test.yaml, got, err)
		}
	}
}

func TestSelfUpdateSettingPrecedence(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yaml")
	host := filepath.Join(dir, "host.yaml")
	if err := os.WriteFile(base, []byte("self_update: {package: dotpkg-bin, origin: aur}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		contents string
		want     SelfUpdate
		invalid  bool
	}{
		{"common: {}\n", SelfUpdate{Package: "dotpkg-bin", Origin: "aur"}, false},
		{"self_update: {package: dotpkg, origin: repo}\n", SelfUpdate{Package: "dotpkg", Origin: "repo"}, false},
		{"self_update: null\n", SelfUpdate{}, true},
		{"self_update: {origin: repo}\n", SelfUpdate{}, true},
	} {
		if err := os.WriteFile(host, []byte(test.contents), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := SelfUpdateSetting(base, host)
		if (err != nil) != test.invalid || !test.invalid && got != test.want {
			t.Errorf("host %q: got %+v, err %v; want %+v", test.contents, got, err, test.want)
		}
	}
}
