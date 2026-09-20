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
	Path     string
	Exists   bool
	Migrated bool
	Data     map[string]any
}

const CurrentVersion = 1

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
	migrated, err := migrate(data)
	if err != nil {
		return nil, fmt.Errorf("migrate state %s: %w", path, err)
	}
	if err := validate(data); err != nil {
		return nil, fmt.Errorf("validate state %s: %w", path, err)
	}
	return &State{Path: path, Exists: true, Migrated: migrated, Data: data}, nil
}

func migrate(data map[string]any) (bool, error) {
	version := 0
	if value, ok := data["version"]; ok {
		switch number := value.(type) {
		case int:
			version = number
		case int64:
			version = int(number)
		case uint:
			version = int(number)
		case uint64:
			version = int(number)
		default:
			return false, fmt.Errorf("version must be an integer")
		}
	}
	if version > CurrentVersion {
		return false, fmt.Errorf("unsupported version %d", version)
	}
	migrated := version != CurrentVersion
	if _, ok := data["current"].(map[string]any); !ok {
		data["current"] = map[string]any{}
		migrated = true
	}
	managed, ok := data["managed"].(map[string]any)
	if !ok {
		managed = map[string]any{}
		data["managed"] = managed
		migrated = true
	}
	for _, name := range []string{"packages", "groups", "configs", "services"} {
		if _, ok := managed[name]; !ok {
			managed[name] = []any{}
			migrated = true
		}
	}
	if _, ok := managed["package_origins"]; !ok {
		managed["package_origins"] = map[string]any{}
		migrated = true
	}
	data["version"] = CurrentVersion
	return migrated, nil
}

func validate(data map[string]any) error {
	if version, ok := data["version"]; ok {
		switch value := version.(type) {
		case int:
			if value != CurrentVersion {
				return fmt.Errorf("unsupported version %d", value)
			}
		case int64:
			if value != CurrentVersion {
				return fmt.Errorf("unsupported version %d", value)
			}
		case uint:
			if value != CurrentVersion {
				return fmt.Errorf("unsupported version %d", value)
			}
		case uint64:
			if value != CurrentVersion {
				return fmt.Errorf("unsupported version %d", value)
			}
		default:
			return fmt.Errorf("version must be 1")
		}
	}
	if current, ok := data["current"]; ok {
		if _, ok := current.(map[string]any); !ok {
			return fmt.Errorf("current must be a mapping")
		}
	}
	managed, ok := data["managed"]
	if !ok {
		return nil
	}
	managedMap, ok := managed.(map[string]any)
	if !ok {
		return fmt.Errorf("managed must be a mapping")
	}
	for _, name := range []string{"packages", "groups", "configs", "services"} {
		if value, exists := managedMap[name]; exists {
			if err := validateStringList(value, "managed."+name); err != nil {
				return err
			}
		}
	}
	if origins, exists := managedMap["package_origins"]; exists {
		originMap, ok := origins.(map[string]any)
		if !ok {
			return fmt.Errorf("managed.package_origins must be a mapping")
		}
		for packageName, origin := range originMap {
			if packageName == "" {
				return fmt.Errorf("managed.package_origins contains an empty package name")
			}
			if value, ok := origin.(string); !ok || (value != "repo" && value != "aur") {
				return fmt.Errorf("managed.package_origins.%s must be repo or aur", packageName)
			}
		}
	}
	return nil
}

func validateStringList(value any, path string) error {
	items, ok := value.([]any)
	if !ok {
		return fmt.Errorf("%s must be a list of strings", path)
	}
	for index, item := range items {
		if text, ok := item.(string); !ok || text == "" {
			return fmt.Errorf("%s[%d] must be a non-empty string", path, index)
		}
	}
	return nil
}

func defaultData() map[string]any {
	return map[string]any{
		"version": 1,
		"current": map[string]any{},
		"managed": map[string]any{
			"packages":        []any{},
			"package_origins": map[string]any{},
			"groups":          []any{},
			"configs":         []any{},
			"services":        []any{},
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
	return s.Items("managed", "packages")
}

func (s *State) SetPackages(packages []string) {
	s.SetItems(packages, "managed", "packages")
}

func (s *State) PackageOrigins() map[string]string {
	value, ok := s.Get("managed", "package_origins")
	if !ok {
		return map[string]string{}
	}
	origins, ok := value.(map[string]any)
	if !ok {
		return map[string]string{}
	}
	result := make(map[string]string, len(origins))
	for packageName, origin := range origins {
		if value, ok := origin.(string); ok {
			result[packageName] = value
		}
	}
	return result
}

func (s *State) SetPackageOrigins(origins map[string]string) {
	values := make(map[string]any, len(origins))
	for packageName, origin := range origins {
		if packageName != "" && (origin == "repo" || origin == "aur") {
			values[packageName] = origin
		}
	}
	s.Set(values, "managed", "package_origins")
}

func (s *State) Items(path ...string) []string {
	value, ok := s.Get(path...)
	if !ok {
		return nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if value, ok := item.(string); ok {
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func (s *State) SetItems(items []string, path ...string) {
	unique := make(map[string]struct{}, len(items))
	for _, item := range items {
		unique[item] = struct{}{}
	}
	result := make([]string, 0, len(unique))
	for item := range unique {
		result = append(result, item)
	}
	sort.Strings(result)
	values := make([]any, len(result))
	for index, item := range result {
		values[index] = item
	}
	s.Set(values, path...)
}

func (s *State) Write() error {
	contents, err := yaml.Marshal(s.Data)
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	if s.Exists {
		if err := backup(s.Path); err != nil {
			return fmt.Errorf("backup state: %w", err)
		}
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

func (s *State) Snapshot() (map[string]any, error) {
	contents, err := yaml.Marshal(s.Data)
	if err != nil {
		return nil, err
	}
	var snapshot map[string]any
	if err := yaml.Unmarshal(contents, &snapshot); err != nil {
		return nil, err
	}
	return snapshot, nil
}

func (s *State) Restore(snapshot map[string]any) {
	s.Data = snapshot
}

func backup(path string) error {
	contents, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	backupPath := path + ".bak"
	temporary, err := os.CreateTemp(filepath.Dir(backupPath), ".dotpkg-state-backup-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, backupPath)
}
