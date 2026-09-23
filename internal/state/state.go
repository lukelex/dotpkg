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

type AppImage struct {
	Address   string
	Target    string
	Algorithm string
	Digest    string
	Version   string
}

type GitHubFile struct {
	Source string
	Target string
	Digest string
}

type GitHubArtifact struct {
	Repo    string
	Ref     string
	Archive string
	Sha256  string
	Files   []GitHubFile
}

const (
	CurrentVersion = 1

	ManagedKey             = "managed"
	ManagedPackages        = "packages"
	ManagedPackageOrigins  = "package_origins"
	ManagedAppImages       = "appimages"
	ManagedGitHubArtifacts = "github"
	ManagedGroups          = "groups"
	ManagedConfigs         = "configs"
	ManagedServices        = "services"
	ManagedExecutableLinks = "executable_links"
	ManagedDirectories     = "directories"
)

var managedListKeys = []string{
	ManagedPackages,
	ManagedGroups,
	ManagedConfigs,
	ManagedServices,
	ManagedExecutableLinks,
	ManagedDirectories,
}

func New(path string) *State {
	return &State{Path: path, Data: defaultData()}
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
			data[ManagedKey].(map[string]any)[ManagedPackages] = packages
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
	managed, ok := data[ManagedKey].(map[string]any)
	if !ok {
		managed = map[string]any{}
		data[ManagedKey] = managed
		migrated = true
	}
	for _, name := range managedListKeys {
		if _, ok := managed[name]; !ok {
			managed[name] = []any{}
			migrated = true
		}
	}
	if _, ok := managed[ManagedPackageOrigins]; !ok {
		managed[ManagedPackageOrigins] = map[string]any{}
		migrated = true
	}
	if _, ok := managed[ManagedAppImages]; !ok {
		managed[ManagedAppImages] = map[string]any{}
		migrated = true
	}
	if _, ok := managed[ManagedGitHubArtifacts]; !ok {
		managed[ManagedGitHubArtifacts] = map[string]any{}
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
	managed, ok := data[ManagedKey]
	if !ok {
		return nil
	}
	managedMap, ok := managed.(map[string]any)
	if !ok {
		return fmt.Errorf("managed must be a mapping")
	}
	for _, name := range managedListKeys {
		if value, exists := managedMap[name]; exists {
			if err := validateStringList(value, "managed."+name); err != nil {
				return err
			}
		}
	}
	if origins, exists := managedMap[ManagedPackageOrigins]; exists {
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
	if appimages, exists := managedMap[ManagedAppImages]; exists {
		appimageMap, ok := appimages.(map[string]any)
		if !ok {
			return fmt.Errorf("managed.appimages must be a mapping")
		}
		for name, value := range appimageMap {
			if name == "" {
				return fmt.Errorf("managed.appimages contains an empty name")
			}
			mapping, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("managed.appimages.%s must be a mapping", name)
			}
			for _, field := range []string{"address", "target", "algorithm", "digest"} {
				if text, ok := mapping[field].(string); !ok || text == "" {
					return fmt.Errorf("managed.appimages.%s.%s must be a non-empty string", name, field)
				}
			}
		}
	}
	if artifacts, exists := managedMap[ManagedGitHubArtifacts]; exists {
		artifactMap, ok := artifacts.(map[string]any)
		if !ok {
			return fmt.Errorf("managed.github must be a mapping")
		}
		for name, value := range artifactMap {
			if name == "" {
				return fmt.Errorf("managed.github contains an empty name")
			}
			mapping, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("managed.github.%s must be a mapping", name)
			}
			for _, field := range []string{"repo", "ref", "archive", "sha256"} {
				if text, ok := mapping[field].(string); !ok || text == "" {
					return fmt.Errorf("managed.github.%s.%s must be a non-empty string", name, field)
				}
			}
			files, ok := mapping["files"].([]any)
			if !ok || len(files) == 0 {
				return fmt.Errorf("managed.github.%s.files must be a non-empty list", name)
			}
			for index, file := range files {
				fields, ok := file.(map[string]any)
				if !ok {
					return fmt.Errorf("managed.github.%s.files[%d] must be a mapping", name, index)
				}
				for _, field := range []string{"source", "target", "digest"} {
					if text, ok := fields[field].(string); !ok || text == "" {
						return fmt.Errorf("managed.github.%s.files[%d].%s must be a non-empty string", name, index, field)
					}
				}
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
		ManagedKey: map[string]any{
			ManagedPackages:        []any{},
			ManagedPackageOrigins:  map[string]any{},
			ManagedAppImages:       map[string]any{},
			ManagedGitHubArtifacts: map[string]any{},
			ManagedGroups:          []any{},
			ManagedConfigs:         []any{},
			ManagedServices:        []any{},
			ManagedExecutableLinks: []any{},
			ManagedDirectories:     []any{},
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
	return s.Items(ManagedKey, ManagedPackages)
}

func (s *State) SetPackages(packages []string) {
	s.SetItems(packages, ManagedKey, ManagedPackages)
}

func (s *State) PackageOrigins() map[string]string {
	value, ok := s.Get(ManagedKey, ManagedPackageOrigins)
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
	s.Set(values, ManagedKey, ManagedPackageOrigins)
}

func (s *State) AppImages() map[string]AppImage {
	value, ok := s.Get(ManagedKey, ManagedAppImages)
	if !ok {
		return map[string]AppImage{}
	}
	mapping, ok := value.(map[string]any)
	if !ok {
		return map[string]AppImage{}
	}
	result := make(map[string]AppImage, len(mapping))
	for name, value := range mapping {
		fields, ok := value.(map[string]any)
		if !ok {
			continue
		}
		record := AppImage{}
		record.Address, _ = fields["address"].(string)
		record.Target, _ = fields["target"].(string)
		record.Algorithm, _ = fields["algorithm"].(string)
		record.Digest, _ = fields["digest"].(string)
		record.Version, _ = fields["version"].(string)
		result[name] = record
	}
	return result
}

func (s *State) SetAppImages(images map[string]AppImage) {
	values := make(map[string]any, len(images))
	for name, image := range images {
		if name == "" || image.Address == "" || image.Target == "" || image.Algorithm == "" || image.Digest == "" {
			continue
		}
		value := map[string]any{
			"address":   image.Address,
			"target":    image.Target,
			"algorithm": image.Algorithm,
			"digest":    image.Digest,
		}
		if image.Version != "" {
			value["version"] = image.Version
		}
		values[name] = value
	}
	s.Set(values, ManagedKey, ManagedAppImages)
}

func (s *State) GitHubArtifacts() map[string]GitHubArtifact {
	value, ok := s.Get(ManagedKey, ManagedGitHubArtifacts)
	if !ok {
		return map[string]GitHubArtifact{}
	}
	mapping, ok := value.(map[string]any)
	if !ok {
		return map[string]GitHubArtifact{}
	}
	result := make(map[string]GitHubArtifact, len(mapping))
	for name, value := range mapping {
		fields, ok := value.(map[string]any)
		if !ok {
			continue
		}
		artifact := GitHubArtifact{}
		artifact.Repo, _ = fields["repo"].(string)
		artifact.Ref, _ = fields["ref"].(string)
		artifact.Archive, _ = fields["archive"].(string)
		artifact.Sha256, _ = fields["sha256"].(string)
		for _, item := range items(fields["files"]) {
			file, ok := item.(map[string]any)
			if !ok {
				continue
			}
			source, sourceOK := file["source"].(string)
			target, targetOK := file["target"].(string)
			digest, digestOK := file["digest"].(string)
			if sourceOK && targetOK && digestOK {
				artifact.Files = append(artifact.Files, GitHubFile{Source: source, Target: target, Digest: digest})
			}
		}
		result[name] = artifact
	}
	return result
}

func (s *State) SetGitHubArtifacts(artifacts map[string]GitHubArtifact) {
	values := make(map[string]any, len(artifacts))
	for name, artifact := range artifacts {
		if name == "" || artifact.Repo == "" || artifact.Ref == "" || artifact.Archive == "" || artifact.Sha256 == "" || len(artifact.Files) == 0 {
			continue
		}
		files := make([]any, 0, len(artifact.Files))
		for _, file := range artifact.Files {
			if file.Source == "" || file.Target == "" || file.Digest == "" {
				continue
			}
			files = append(files, map[string]any{"source": file.Source, "target": file.Target, "digest": file.Digest})
		}
		if len(files) > 0 {
			values[name] = map[string]any{"repo": artifact.Repo, "ref": artifact.Ref, "archive": artifact.Archive, "sha256": artifact.Sha256, "files": files}
		}
	}
	s.Set(values, ManagedKey, ManagedGitHubArtifacts)
}

func items(value any) []any {
	items, _ := value.([]any)
	return items
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
