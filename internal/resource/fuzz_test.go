package resource

import (
	"path/filepath"
	"testing"
)

func FuzzUserDirectoryPath(f *testing.F) {
	f.Add("$HOME/projects")
	f.Add("$XDG_CONFIG_HOME/tool")
	f.Add("~/screenshots")
	f.Add("/etc/not-allowed")

	f.Fuzz(func(t *testing.T, value string) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
		path, err := userDirectoryPath(value)
		if err != nil {
			return
		}
		if !filepath.IsAbs(path) {
			t.Fatalf("resolved non-absolute path %q from %q", path, value)
		}
		if !pathWithin(home, path) {
			t.Fatalf("resolved path outside home %q from %q", path, value)
		}
	})
}
