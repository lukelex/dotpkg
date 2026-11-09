package manifest

import "fmt"

// ServiceDependency describes one input read by a service. Relative paths are
// resolved against the resource root. Package dependencies are restart-only.
type ServiceDependency struct {
	Path    string
	Package string
	Action  string
}

func validateServiceDependencies(value any) error {
	items, ok := value.([]any)
	if !ok {
		return fmt.Errorf("dependencies must be a list")
	}
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("dependency must be a mapping")
		}
		path, _ := m["path"].(string)
		pkg, _ := m["package"].(string)
		for _, key := range []string{"path", "package"} {
			if value, exists := m[key]; exists {
				if text, ok := value.(string); !ok || text == "" {
					return fmt.Errorf("dependency %s must be a non-empty string", key)
				}
			}
		}
		if (path == "") == (pkg == "") {
			return fmt.Errorf("dependency requires exactly one non-empty path or package")
		}
		for key := range m {
			if key != "path" && key != "package" && key != "action" {
				return fmt.Errorf("unknown dependency field %s", key)
			}
		}
		if a, exists := m["action"]; exists {
			if a != "restart" && a != "reload" {
				return fmt.Errorf("dependency action must be restart or reload")
			}
			if pkg != "" && a != "restart" {
				return fmt.Errorf("package dependencies require restart")
			}
		}
	}
	return nil
}

// ServiceDependencies returns packages associated with declared services.
func (m *Manifest) ServiceDependencies(paths []string, profile string, selections map[string]bool) map[string][]ServiceDependency {
	result := map[string][]ServiceDependency{}
	for _, path := range paths {
		var walk func(any)
		walk = func(value any) {
			mapping, ok := value.(map[string]any)
			if !ok {
				return
			}
			for name, child := range mapping {
				metadata, ok := child.(map[string]any)
				if !ok {
					continue
				}
				if services, ok := metadata["services"].(map[string]any); ok {
					for _, scope := range []string{"user", "system"} {
						items, _ := services[scope].([]any)
						for _, item := range items {
							if service, ok := item.(string); ok {
								key := resourceServiceName(scope, service)
								result[key] = append(result[key], ServiceDependency{Package: name, Action: "restart"})
							}
						}
					}
				} else {
					walk(child)
				}
			}
		}
		walk(m.Value(path))
	}
	resources, _ := m.Data["resources"].(map[string]any)
	items, _ := resources["services"].([]any)
	for _, item := range items {
		mapping, ok := item.(map[string]any)
		if !ok || !resourceSelected(mapping, profile, selections) {
			continue
		}
		name, _ := mapping["name"].(string)
		scope, _ := mapping["scope"].(string)
		key := resourceServiceName(scope, name)
		deps, _ := mapping["dependencies"].([]any)
		for _, dep := range deps {
			d, _ := dep.(map[string]any)
			path, _ := d["path"].(string)
			pkg, _ := d["package"].(string)
			action, _ := d["action"].(string)
			if action == "" {
				action = "restart"
			}
			result[key] = append(result[key], ServiceDependency{Path: path, Package: pkg, Action: action})
		}
	}
	return result
}
