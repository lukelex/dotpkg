package state

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type State struct {
	Path   string
	Exists bool
	Data   map[string]any
}

func Load(path string) (*State, error) {
	contents, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		data := defaultData()
		legacyPath := filepath.Join(filepath.Dir(path), "packages")
		if legacy, legacyErr := os.ReadFile(legacyPath); legacyErr == nil {
			packages := []any{}
			for _, line := range strings.Split(string(legacy), "\n") {
				if packageName := strings.TrimSpace(line); packageName != "" {
					packages = append(packages, packageName)
				}
			}
			data["managed"].(map[string]any)["packages"] = packages
		}
		return &State{Path: path, Data: data}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state %s: %w", path, err)
	}
	var data map[string]any
	if err := yaml.Unmarshal(contents, &data); err != nil {
		return nil, fmt.Errorf("parse state %s: %w", path, err)
	}
	if data == nil {
		data = map[string]any{}
	}
	return &State{Path: path, Exists: true, Data: data}, nil
}

func defaultData() map[string]any {
	return map[string]any{
		"version": 1,
		"current": map[string]any{},
		"managed": map[string]any{
			"packages": []any{},
			"groups":   []any{},
			"configs":  []any{},
			"services": []any{},
		},
	}
}

func (s *State) Get(path ...string) (any, bool) {
	var current any = s.Data
	for _, part := range path {
		mapping, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = mapping[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func (s *State) Set(value any, path ...string) {
	if len(path) == 0 {
		return
	}
	mapping := s.Data
	for _, part := range path[:len(path)-1] {
		next, ok := mapping[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			mapping[part] = next
		}
		mapping = next
	}
	mapping[path[len(path)-1]] = value
}

func (s *State) Selection(path ...string) (bool, bool) {
	value, ok := s.Get(append([]string{"current", "selections"}, path...)...)
	selected, isBool := value.(bool)
	return selected, ok && isBool
}

func (s *State) Packages() []string {
	value, ok := s.Get("managed", "packages")
	if !ok {
		return nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	packages := make([]string, 0, len(items))
	for _, item := range items {
		if packageName, ok := item.(string); ok {
			packages = append(packages, packageName)
		}
	}
	sort.Strings(packages)
	return packages
}

func (s *State) SetPackages(packages []string) {
	unique := make(map[string]struct{}, len(packages))
	for _, packageName := range packages {
		unique[packageName] = struct{}{}
	}
	result := make([]string, 0, len(unique))
	for packageName := range unique {
		result = append(result, packageName)
	}
	sort.Strings(result)
	values := make([]any, len(result))
	for index, packageName := range result {
		values[index] = packageName
	}
	s.Set(values, "managed", "packages")
}

func (s *State) Write() error {
	contents, err := yaml.Marshal(s.Data)
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(s.Path), ".dotpkg-state-*")
	if err != nil {
		return fmt.Errorf("create temporary state: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary state: %w", err)
	}
	if err := os.Rename(temporaryName, s.Path); err != nil {
		return fmt.Errorf("replace state: %w", err)
	}
	s.Exists = true
	return nil
}
