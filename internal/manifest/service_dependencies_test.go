package manifest

import "testing"

func TestServiceDependencyValidation(t *testing.T) {
	for _, test := range []struct {
		value any
		valid bool
	}{
		{[]any{map[string]any{"path": "config", "action": "reload"}}, true},
		{[]any{map[string]any{"package": "tool"}}, true},
		{[]any{map[string]any{"package": "tool", "action": "reload"}}, false},
		{[]any{map[string]any{"path": "config", "package": "tool"}}, false},
		{[]any{map[string]any{"path": "config", "action": "typo"}}, false},
		{[]any{map[string]any{"paths": "config"}}, false},
		{"config", false},
	} {
		if err := validateServiceDependencies(test.value); (err == nil) != test.valid {
			t.Errorf("value=%v error=%v", test.value, err)
		}
	}
}

func TestServiceDependenciesRetainPackageAssociationAndFilters(t *testing.T) {
	m := &Manifest{Data: map[string]any{
		"common": map[string]any{"packages": map[string]any{"headless": map[string]any{"tool": map[string]any{"services": map[string]any{"user": []any{"tool.service"}}}}}},
		"resources": map[string]any{"services": []any{
			map[string]any{"name": "tool.service", "scope": "user", "dependencies": []any{map[string]any{"path": "config"}}},
			map[string]any{"name": "excluded.service", "scope": "system", "profiles": []any{"server"}, "dependencies": []any{map[string]any{"path": "secret"}}},
		}},
	}}
	deps := m.ServiceDependencies([]string{"common.packages"}, "desktop", nil)
	if len(deps) != 1 || len(deps["user:tool.service"]) != 2 || deps["user:tool.service"][0].Package != "tool" {
		t.Fatalf("dependencies=%+v", deps)
	}
}
