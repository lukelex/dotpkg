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
		got, err := HostSelfUpdate(path)
		if (err == nil) != test.ok {
			t.Errorf("%q: setting=%+v err=%v", test.yaml, got, err)
		}
	}
}
