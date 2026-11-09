package backend

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

func TestServiceActivityDistinguishesInactiveFromErrors(t *testing.T) {
	for _, test := range []struct {
		code           int
		active, failed bool
	}{{0, true, false}, {3, false, false}, {4, false, false}, {1, false, true}} {
		runner := &fakeRunner{outputs: map[string]fakeOutput{}}
		if test.code != 0 {
			runner.outputs["systemctl"] = fakeOutput{err: fakeExitError{test.code}}
		}
		n := nativeResources{Runner: runner}
		active, err := n.ServiceActive(context.Background(), true, "tool.service")
		if active != test.active || (err != nil) != test.failed {
			t.Fatalf("code=%d active=%v err=%v", test.code, active, err)
		}
		if !reflect.DeepEqual(runner.calls[0].args, []string{"--user", "is-active", "--quiet", "tool.service"}) {
			t.Fatal(runner.calls)
		}
	}
}

func TestServiceFilesIncludeEffectiveAndAbsentOverrideInputs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	runner := &fakeRunner{outputs: map[string]fakeOutput{"systemctl": {value: []byte("FragmentPath=/opt/tool.service\nDropInPaths=/opt/override.conf\n")}}}
	n := nativeResources{Runner: runner}
	paths, err := n.ServiceFiles(context.Background(), true, "tool.service")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"/opt/tool.service", "/opt/override.conf", filepath.Join(home, "config/systemd/user/tool.service.d")} {
		found := false
		for _, path := range paths {
			found = found || path == want
		}
		if !found {
			t.Errorf("missing %s in %v", want, paths)
		}
	}
	if err := n.ReloadService(context.Background(), false, "tool.service"); err != nil {
		t.Fatal(err)
	}
	last := runner.calls[len(runner.calls)-1]
	if last.name != "sudo" || !reflect.DeepEqual(last.args, []string{"systemctl", "reload", "tool.service"}) {
		t.Fatal(last)
	}
}
