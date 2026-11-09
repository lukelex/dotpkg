package manifest

import (
	"os"
	"path/filepath"
	"testing"
)

func FuzzLoad(f *testing.F) {
	f.Add([]byte("source: repo\n"))
	f.Add([]byte("resources:\n  directories:\n    - $HOME/projects\n"))
	f.Add([]byte("profiles:\n  desktop:\n    packages:\n      desktop:\n        git:\n"))

	f.Fuzz(func(t *testing.T, contents []byte) {
		path := filepath.Join(t.TempDir(), "packages.yaml")
		if err := os.WriteFile(path, contents, 0o644); err != nil {
			t.Fatal(err)
		}
		_, _ = Load(path, "")
	})
}
