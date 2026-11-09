package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoteBundleDeclaration(t *testing.T) {
	const declaration = `resources:
  remote_bundles:
    - name: grill-me
      repository: mattpocock/skills
      paths: [skills/grill-me, skills/grilling, LICENSE]
      target: $HOME/.local/share/dotpkg/opencode-skills
`
	path := filepath.Join(t.TempDir(), "packages.yaml")
	for _, test := range []struct {
		name, data string
		valid      bool
	}{
		{"unpinned", declaration, true},
		{"pinned", declaration + "      commit: 4ae2317ddb22b046e26455e4f9c9b3aaaf62212d\n", true},
		{"bad pin", declaration + "      commit: main\n", false},
		{"other repo", strings.Replace(declaration, "mattpocock/skills", "someone/skills", 1), true},
		{"other path", strings.Replace(declaration, "skills/grilling", "skills/other", 1), true},
		{"escape path", strings.Replace(declaration, "skills/grilling", "../secrets", 1), false},
		{"duplicate destination", strings.Replace(declaration, "skills/grilling", "other/grill-me", 1), false},
		{"other target", strings.Replace(declaration, ".local/share/dotpkg", "dotfiles", 1), false},
		{"unknown key", declaration + "      command: curl something\n", false},
		{"overlapping targets", declaration + "    - name: second\n      repository: example/project\n      paths: [README.md]\n      target: $HOME/.local/share/dotpkg/opencode-skills/child\n", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(test.data), 0o644); err != nil {
				t.Fatal(err)
			}
			m, err := Load(path, "")
			if (err == nil) != test.valid {
				t.Fatalf("load error = %v", err)
			}
			if test.valid && (len(m.RemoteBundles()) != 1 || m.RemoteBundles()[0].Name != "grill-me") {
				t.Fatal(m.RemoteBundles())
			}
		})
	}
}
