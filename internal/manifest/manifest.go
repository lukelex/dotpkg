package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Manifest contains the effective manifest after an optional host overlay has
// been deep-merged into the base manifest.
type Manifest struct {
	Path string
	Data map[string]any
}

func Load(path, hostPath string) (*Manifest, error) {
	base, err := readMap(path)
	if err != nil {
		return nil, err
	}
	if hostPath != "" {
		overlay, err := readMap(hostPath)
		if err != nil {
			return nil, err
		}
		base = mergeMaps(base, overlay)
	}
	if err := validate(base); err != nil {
		return nil, fmt.Errorf("validate manifest %s: %w", path, err)
	}
	return &Manifest{Path: path, Data: base}, nil
}

func readMap(path string) (map[string]any, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest %s: %w", path, err)
	}
	var value any
	if err := yaml.Unmarshal(contents, &value); err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", path, err)
	}
	result, ok := normalize(value).(map[string]any)
	if !ok {
		return nil, fmt.Errorf("manifest %s must contain a mapping at the root", path)
	}
	return result, nil
}

func normalize(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, child := range typed {
			result[key] = normalize(child)
		}
		return result
	case map[any]any:
		result := make(map[string]any, len(typed))
		for key, child := range typed {
			result[fmt.Sprint(key)] = normalize(child)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, child := range typed {
			result[index] = normalize(child)
		}
		return result
	default:
		return value
	}
}

func mergeMaps(base, overlay map[string]any) map[string]any {
	result := make(map[string]any, len(base)+len(overlay))
	for key, value := range base {
		result[key] = value
	}
	for key, value := range overlay {
		baseMap, baseOK := result[key].(map[string]any)
		overlayMap, overlayOK := value.(map[string]any)
		if baseOK && overlayOK {
			result[key] = mergeMaps(baseMap, overlayMap)
		} else {
			result[key] = value
		}
	}
	return result
}

func validate(data map[string]any) error {
	if value, ok := data["source"]; ok {
		if err := validateSource(value, "source"); err != nil {
			return err
		}
	}
	if value, ok := data["common"]; ok {
		if err := validateProfileSection(value, "common"); err != nil {
			return err
		}
	}
	if value, ok := data["profiles"]; ok {
		profiles, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("profiles must be a mapping")
		}
		for name, profile := range profiles {
			if err := validateProfileSection(profile, "profiles."+name); err != nil {
				return err
			}
		}
	}
	if value, ok := data["resources"]; ok {
		if err := validateResources(value); err != nil {
			return err
		}
	}
	return nil
}

func validateProfileSection(value any, path string) error {
	section, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%s must be a mapping", path)
	}
	if packages, ok := section["packages"]; ok {
		if err := validatePackageSection(packages, path+".packages"); err != nil {
			return err
		}
	}
	if options, ok := section["options"]; ok {
		if err := validateOptions(options, path+".options"); err != nil {
			return err
		}
	}
	return nil
}

func validatePackageSection(value any, path string) error {
	section, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%s must be a mapping", path)
	}
	for category, packages := range section {
		var err error
		if mapping, ok := packages.(map[string]any); ok && isMetadataMapping(mapping) {
			err = validatePackageValue(packages, path+"."+category)
		} else {
			err = validatePackageMap(packages, path+"."+category)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func isMetadataMapping(value map[string]any) bool {
	for _, key := range []string{"source", "address", "target", "repo", "ref", "archive", "files", "groups", "configs", "services", "install"} {
		if _, ok := value[key]; ok {
			return true
		}
	}
	return false
}

func validatePackageMap(value any, path string) error {
	packages, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%s must be a package mapping", path)
	}
	for name, packageValue := range packages {
		if err := validatePackageValue(packageValue, path+"."+name); err != nil {
			return err
		}
	}
	return nil
}

func validatePackageValue(value any, path string) error {
	if value == nil {
		return nil
	}
	mapping, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%s must be a package metadata mapping or null", path)
	}
	source, _ := mapping["source"].(string)
	for key, child := range mapping {
		fieldPath := path + "." + key
		switch key {
		case "source":
			if err := validateSource(child, fieldPath); err != nil {
				return err
			}
		case "address", "target", "repo", "ref", "archive":
			if text, ok := child.(string); !ok || strings.TrimSpace(text) == "" {
				return fmt.Errorf("%s must be a non-empty string", fieldPath)
			}
		case "groups":
			if err := validateStringList(child, fieldPath); err != nil {
				return err
			}
		case "configs":
			if err := validateConfigs(child, fieldPath); err != nil {
				return err
			}
		case "services":
			if err := validatePackageServices(child, fieldPath); err != nil {
				return err
			}
		case "install":
			if _, ok := child.(bool); !ok {
				return fmt.Errorf("%s must be a boolean", fieldPath)
			}
		case "sha256":
			if err := validateSha256(child, fieldPath); err != nil {
				return err
			}
		case "files":
			if err := validateGitHubFiles(child, fieldPath); err != nil {
				return err
			}
		}
	}
	if source == "appimage" {
		address, ok := mapping["address"].(string)
		if !ok || strings.TrimSpace(address) == "" {
			return fmt.Errorf("%s.address is required for AppImage packages", path)
		}
		parsed, err := url.Parse(address)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			return fmt.Errorf("%s.address must be an HTTPS URL", path)
		}
		for _, segment := range strings.Split(strings.Trim(parsed.Path, "/"), "/") {
			if strings.EqualFold(segment, "latest") {
				return fmt.Errorf("%s.address must pin a release, not latest", path)
			}
		}
	}
	if source == "github" {
		if err := validateGitHubPackage(mapping, path); err != nil {
			return err
		}
	}
	return nil
}

func validateGitHubPackage(mapping map[string]any, path string) error {
	repo, _ := mapping["repo"].(string)
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || !githubName(parts[0]) || !githubName(parts[1]) {
		return fmt.Errorf("%s.repo must be an owner/repository name", path)
	}
	ref, _ := mapping["ref"].(string)
	if floatingGitHubRef(ref) || !githubRef(ref) {
		return fmt.Errorf("%s.ref must be a pinned tag or commit, not %q", path, ref)
	}
	if _, ok := mapping["archive"].(string); !ok {
		return fmt.Errorf("%s.archive is required for GitHub packages", path)
	}
	archive := mapping["archive"].(string)
	if archive != "source" && (!safeArchivePath(archive) || strings.Contains(archive, "/")) {
		return fmt.Errorf("%s.archive must be source or a release asset file name", path)
	}
	if _, ok := mapping["sha256"]; !ok {
		return fmt.Errorf("%s.sha256 is required for GitHub packages", path)
	}
	files, ok := mapping["files"].([]any)
	if !ok || len(files) == 0 {
		return fmt.Errorf("%s.files must be a non-empty list", path)
	}
	return nil
}

func githubRef(value string) bool {
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '-' || character == '_' || character == '.') {
			return false
		}
	}
	return true
}

func githubName(value string) bool {
	if value == "" || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") {
		return false
	}
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '-' || character == '_' || character == '.') {
			return false
		}
	}
	return true
}

func floatingGitHubRef(ref string) bool {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.Contains(ref, "/") || strings.Contains(ref, "\\") || strings.Contains(ref, "..") {
		return true
	}
	switch strings.ToLower(ref) {
	case "main", "master", "head", "latest", "stable", "development", "develop":
		return true
	}
	return false
}

func validateGitHubFiles(value any, path string) error {
	items, ok := value.([]any)
	if !ok || len(items) == 0 {
		return fmt.Errorf("%s must be a non-empty list", path)
	}
	targets := make(map[string]struct{}, len(items))
	for index, item := range items {
		itemPath := fmt.Sprintf("%s[%d]", path, index)
		mapping, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be a mapping", itemPath)
		}
		for _, field := range []string{"source", "target"} {
			text, ok := mapping[field].(string)
			if !ok || strings.TrimSpace(text) == "" {
				return fmt.Errorf("%s.%s must be a non-empty string", itemPath, field)
			}
			if field == "source" && !safeArchivePath(text) {
				return fmt.Errorf("%s.source must be a relative path without traversal", itemPath)
			}
		}
		target := mapping["target"].(string)
		if !safeGitHubTarget(target) {
			return fmt.Errorf("%s.target must be a safe path inside $HOME", itemPath)
		}
		if _, duplicate := targets[target]; duplicate {
			return fmt.Errorf("%s.target is duplicated: %s", itemPath, target)
		}
		targets[target] = struct{}{}
	}
	return nil
}

func safeGitHubTarget(value string) bool {
	if !strings.HasPrefix(value, "$HOME/") {
		return false
	}
	for _, part := range strings.Split(strings.TrimPrefix(value, "$HOME/"), "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func safeArchivePath(value string) bool {
	value = strings.ReplaceAll(value, "\\", "/")
	if value == "" || strings.HasPrefix(value, "/") {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func validateOptions(value any, path string) error {
	options, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%s must be a mapping", path)
	}
	for name, value := range options {
		optionPath := path + "." + name
		option, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be a mapping", optionPath)
		}
		for key, child := range option {
			fieldPath := optionPath + "." + key
			switch key {
			case "packages":
				if err := validatePackageMap(child, fieldPath); err != nil {
					return err
				}
			case "prompt":
				if _, ok := child.(string); !ok {
					return fmt.Errorf("%s must be a string", fieldPath)
				}
			case "default":
				if !isBoolean(child) {
					return fmt.Errorf("%s must be a boolean", fieldPath)
				}
			}
		}
	}
	return nil
}

func isBoolean(value any) bool {
	if _, ok := value.(bool); ok {
		return true
	}
	text, ok := value.(string)
	return ok && (strings.EqualFold(text, "yes") || strings.EqualFold(text, "no"))
}

func validateResources(value any) error {
	resources, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("resources must be a mapping")
	}
	if configs, ok := resources["configs"]; ok {
		if err := validateConfigs(configs, "resources.configs"); err != nil {
			return err
		}
	}
	if services, ok := resources["services"]; ok {
		if err := validateResourceServices(services); err != nil {
			return err
		}
	}
	if links, ok := resources["executable_links"]; ok {
		if err := validateExecutableLinks(links); err != nil {
			return err
		}
	}
	if directories, ok := resources["directories"]; ok {
		if err := validateDirectories(directories); err != nil {
			return err
		}
	}
	return nil
}

func validateDirectories(value any) error {
	items, ok := value.([]any)
	if !ok {
		return fmt.Errorf("resources.directories must be a list")
	}
	for index, item := range items {
		path := fmt.Sprintf("resources.directories[%d]", index)
		switch item := item.(type) {
		case string:
			if item == "" {
				return fmt.Errorf("%s must be a non-empty path", path)
			}
		case map[string]any:
			directory, ok := item["path"].(string)
			if !ok || directory == "" {
				return fmt.Errorf("%s.path must be a non-empty string", path)
			}
			if err := validateFilters(item, path); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%s must be a path string or resource mapping", path)
		}
	}
	return nil
}

func validateExecutableLinks(value any) error {
	items, ok := value.([]any)
	if !ok {
		return fmt.Errorf("resources.executable_links must be a list")
	}
	for index, item := range items {
		path := fmt.Sprintf("resources.executable_links[%d]", index)
		mapping, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be a mapping", path)
		}
		for _, field := range []string{"source", "target"} {
			if value, ok := mapping[field].(string); !ok || value == "" {
				return fmt.Errorf("%s.%s must be a non-empty string", path, field)
			}
		}
		if prefix, ok := mapping["prefix"]; ok {
			if _, ok := prefix.(string); !ok {
				return fmt.Errorf("%s.prefix must be a string", path)
			}
		}
		if prune, ok := mapping["prune"]; ok && !isBoolean(prune) {
			return fmt.Errorf("%s.prune must be a boolean", path)
		}
		if err := validateFilters(mapping, path); err != nil {
			return err
		}
	}
	return nil
}

func validateConfigs(value any, path string) error {
	items, ok := value.([]any)
	if !ok {
		return fmt.Errorf("%s must be a list", path)
	}
	for index, item := range items {
		itemPath := fmt.Sprintf("%s[%d]", path, index)
		switch mapping := item.(type) {
		case string:
			if err := validateConfigMapping(mapping, itemPath); err != nil {
				return err
			}
		case map[string]any:
			source, sourceOK := mapping["source"].(string)
			target, targetOK := mapping["target"].(string)
			if !sourceOK || source == "" {
				return fmt.Errorf("%s.source must be a non-empty string", itemPath)
			}
			if !targetOK || target == "" {
				return fmt.Errorf("%s.target must be a non-empty string", itemPath)
			}
			if err := validateFilters(mapping, itemPath); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%s must be a mapping string or resource mapping", itemPath)
		}
	}
	return nil
}

func validateConfigMapping(value, path string) error {
	separator := strings.IndexByte(value, ':')
	if separator <= 0 || separator == len(value)-1 {
		return fmt.Errorf("%s must use source:target form", path)
	}
	return nil
}

func validatePackageServices(value any, path string) error {
	services, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%s must be a mapping", path)
	}
	for scope, names := range services {
		if scope != "system" && scope != "user" {
			return fmt.Errorf("%s.%s is not a supported service scope", path, scope)
		}
		if err := validateStringList(names, path+"."+scope); err != nil {
			return err
		}
	}
	return nil
}

func validateResourceServices(value any) error {
	if mapping, ok := value.(map[string]any); ok {
		for scope, names := range mapping {
			if scope != "system" && scope != "user" {
				return fmt.Errorf("resources.services.%s is not a supported service scope", scope)
			}
			if err := validateStringList(names, "resources.services."+scope); err != nil {
				return err
			}
		}
		return nil
	}
	items, ok := value.([]any)
	if !ok {
		return fmt.Errorf("resources.services must be a mapping or list")
	}
	for index, item := range items {
		path := fmt.Sprintf("resources.services[%d]", index)
		mapping, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be a service resource mapping", path)
		}
		name, nameOK := mapping["name"].(string)
		if !nameOK || name == "" {
			return fmt.Errorf("%s.name must be a non-empty string", path)
		}
		scope, scopeOK := mapping["scope"].(string)
		if !scopeOK || (scope != "system" && scope != "user") {
			return fmt.Errorf("%s.scope must be system or user", path)
		}
		if err := validateFilters(mapping, path); err != nil {
			return err
		}
	}
	return nil
}

func validateFilters(mapping map[string]any, path string) error {
	for _, key := range []string{"profiles", "selections"} {
		if value, ok := mapping[key]; ok {
			if err := validateStringList(value, path+"."+key); err != nil {
				return err
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

func validateSource(value any, path string) error {
	source, ok := value.(string)
	if !ok || (source != "repo" && source != "aur" && source != "appimage" && source != "github") {
		return fmt.Errorf("%s must be repo, aur, appimage, or github", path)
	}
	return nil
}

func validateSha256(value any, path string) error {
	text, ok := value.(string)
	if !ok {
		return fmt.Errorf("%s must be a string", path)
	}
	if len(text) != 64 {
		return fmt.Errorf("%s must be a 64-character SHA256 digest", path)
	}
	if _, err := hex.DecodeString(text); err != nil {
		return fmt.Errorf("%s must be a hexadecimal SHA256 digest", path)
	}
	return nil
}

// AppImageSpec is the manifest metadata needed to install one AppImage.
type AppImageSpec struct {
	Name    string
	Address string
	Target  string
	Sha256  string
}

// GitHubFile maps one regular file in a GitHub archive to a user-owned target.
type GitHubFile struct {
	Source string
	Target string
}

// GitHubSpec describes a pinned GitHub archive or release asset.
type GitHubSpec struct {
	Name    string
	Repo    string
	Ref     string
	Archive string
	Sha256  string
	Files   []GitHubFile
}

func (m *Manifest) GitHub(name string) (GitHubSpec, bool) {
	var result GitHubSpec
	found := false
	var walk func(any)
	walk = func(value any) {
		if found {
			return
		}
		mapping, ok := value.(map[string]any)
		if !ok {
			return
		}
		if packageValue, exists := mapping[name]; exists {
			if metadata, ok := packageValue.(map[string]any); ok {
				if source, _ := metadata["source"].(string); source == "github" {
					result = GitHubSpec{Name: name}
					result.Repo, _ = metadata["repo"].(string)
					result.Ref, _ = metadata["ref"].(string)
					result.Archive, _ = metadata["archive"].(string)
					result.Sha256, _ = metadata["sha256"].(string)
					for _, item := range metadata["files"].([]any) {
						file, ok := item.(map[string]any)
						if !ok {
							continue
						}
						source, sourceOK := file["source"].(string)
						target, targetOK := file["target"].(string)
						if sourceOK && targetOK {
							result.Files = append(result.Files, GitHubFile{Source: source, Target: target})
						}
					}
					found = true
				}
			}
		}
		for _, child := range mapping {
			walk(child)
		}
	}
	walk(m.Data)
	return result, found
}

func (m *Manifest) GitHubs(paths []string) []GitHubSpec {
	var result []GitHubSpec
	seen := make(map[string]struct{})
	for _, path := range paths {
		for _, name := range m.PackageNames(path) {
			if _, already := seen[name]; already {
				continue
			}
			if spec, ok := m.GitHub(name); ok {
				result = append(result, spec)
				seen[name] = struct{}{}
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

// AppImage returns the AppImage metadata attached to a package node.
func (m *Manifest) AppImage(name string) (AppImageSpec, bool) {
	var result AppImageSpec
	found := false
	var walk func(any)
	walk = func(value any) {
		if found {
			return
		}
		mapping, ok := value.(map[string]any)
		if !ok {
			return
		}
		if packageValue, exists := mapping[name]; exists {
			if metadata, ok := packageValue.(map[string]any); ok {
				if source, _ := metadata["source"].(string); source == "appimage" {
					address, addressOK := metadata["address"].(string)
					if addressOK && address != "" {
						result = AppImageSpec{Name: name, Address: address}
						result.Target, _ = metadata["target"].(string)
						result.Sha256, _ = metadata["sha256"].(string)
						found = true
					}
				}
			}
		}
		for _, child := range mapping {
			walk(child)
		}
	}
	walk(m.Data)
	return result, found
}

// AppImages returns AppImage package nodes declared in the selected paths.
func (m *Manifest) AppImages(paths []string) []AppImageSpec {
	var result []AppImageSpec
	seen := make(map[string]struct{})
	for _, path := range paths {
		for _, name := range m.PackageNames(path) {
			if _, already := seen[name]; already {
				continue
			}
			if spec, ok := m.AppImage(name); ok {
				result = append(result, spec)
				seen[name] = struct{}{}
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (m *Manifest) Source() string {
	if source, ok := m.Data["source"].(string); ok && source != "" {
		return source
	}
	return "aur"
}

// Digest is the stable digest used in the shared dotfiles state contract.
func (m *Manifest) Digest() string {
	contents, err := yaml.Marshal(m.Data)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:])
}

func (m *Manifest) Value(path string) any {
	var current any = m.Data
	for _, part := range splitPath(path) {
		mapping, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current, ok = mapping[part]
		if !ok {
			return nil
		}
	}
	return current
}

func (m *Manifest) PackageNames(path string) []string {
	mapping, ok := m.Value(path).(map[string]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(mapping))
	for name := range mapping {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func (m *Manifest) OptionNames() []string {
	return m.PackageNames("profiles.desktop.options")
}

func (m *Manifest) OptionPackages(option string) []string {
	return m.PackageNames("profiles.desktop.options." + option + ".packages")
}

func (m *Manifest) OptionPrompt(option string) (string, bool) {
	value, ok := m.Value("profiles.desktop.options." + option + ".prompt").(string)
	return value, ok && value != ""
}

func (m *Manifest) OptionDefault(option string) bool {
	value := m.Value("profiles.desktop.options." + option + ".default")
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(typed, "yes") || strings.EqualFold(typed, "true")
	default:
		return false
	}
}

func (m *Manifest) MetadataStrings(path, key string) []string {
	return metadataStrings(m.Value(path), key)
}

func (m *Manifest) ServiceNames(path, scope string) []string {
	return serviceNames(m.Value(path), scope)
}

// ResourceConfigs returns explicit config links that are not attached to a
// package. Entries may be mapping strings or objects with source, target,
// profiles, and selections fields.
func (m *Manifest) ResourceConfigs(profile string, selections map[string]bool) []string {
	resources, ok := m.Data["resources"].(map[string]any)
	if !ok {
		return nil
	}
	items, ok := resources["configs"].([]any)
	if !ok {
		return nil
	}
	var result []string
	for _, item := range items {
		switch value := item.(type) {
		case string:
			result = append(result, value)
		case map[string]any:
			if !resourceSelected(value, profile, selections) {
				continue
			}
			source, sourceOK := value["source"].(string)
			target, targetOK := value["target"].(string)
			if sourceOK && targetOK && source != "" && target != "" {
				result = append(result, source+":"+target)
			}
		}
	}
	return uniqueStrings(result)
}

// ResourceDirectories returns explicit user directories, optionally filtered
// by profile and selections. Entries may be path strings or mappings with a
// path, profiles, and selections field.
func (m *Manifest) ResourceDirectories(profile string, selections map[string]bool) []string {
	resources, ok := m.Data["resources"].(map[string]any)
	if !ok {
		return nil
	}
	items, ok := resources["directories"].([]any)
	if !ok {
		return nil
	}
	var result []string
	for _, item := range items {
		switch value := item.(type) {
		case string:
			result = append(result, value)
		case map[string]any:
			if !resourceSelected(value, profile, selections) {
				continue
			}
			if path, ok := value["path"].(string); ok && path != "" {
				result = append(result, path)
			}
		}
	}
	return uniqueStrings(result)
}

// ExecutableLinkCollection describes a directory whose executable files are
// linked into a target directory.
type ExecutableLinkCollection struct {
	Source string
	Target string
	Prefix string
	Prune  bool
}

func (m *Manifest) ExecutableLinkCollections(profile string, selections map[string]bool) []ExecutableLinkCollection {
	resources, ok := m.Data["resources"].(map[string]any)
	if !ok {
		return nil
	}
	items, ok := resources["executable_links"].([]any)
	if !ok {
		return nil
	}
	var result []ExecutableLinkCollection
	for _, item := range items {
		mapping, ok := item.(map[string]any)
		if !ok || !resourceSelected(mapping, profile, selections) {
			continue
		}
		collection := ExecutableLinkCollection{Prefix: ""}
		collection.Source, _ = mapping["source"].(string)
		collection.Target, _ = mapping["target"].(string)
		collection.Prefix, _ = mapping["prefix"].(string)
		switch prune := mapping["prune"].(type) {
		case bool:
			collection.Prune = prune
		case string:
			collection.Prune = strings.EqualFold(prune, "yes") || strings.EqualFold(prune, "true")
		}
		result = append(result, collection)
	}
	return result
}

// ResourceServices returns explicit services that are not attached to a
// package. Service objects use name, scope, profiles, and selections fields.
func (m *Manifest) ResourceServices(profile string, selections map[string]bool) []string {
	resources, ok := m.Data["resources"].(map[string]any)
	if !ok {
		return nil
	}
	services, ok := resources["services"]
	if !ok {
		return nil
	}
	var result []string
	if mapping, ok := services.(map[string]any); ok {
		for _, scope := range []string{"system", "user"} {
			items, ok := mapping[scope].([]any)
			if !ok {
				continue
			}
			for _, item := range items {
				if name, ok := item.(string); ok {
					result = append(result, resourceServiceName(scope, name))
				}
			}
		}
	}
	if items, ok := services.([]any); ok {
		for _, item := range items {
			mapping, ok := item.(map[string]any)
			if !ok || !resourceSelected(mapping, profile, selections) {
				continue
			}
			name, nameOK := mapping["name"].(string)
			scope, scopeOK := mapping["scope"].(string)
			if nameOK && scopeOK && name != "" && (scope == "system" || scope == "user") {
				result = append(result, resourceServiceName(scope, name))
			}
		}
	}
	return uniqueStrings(result)
}

func resourceServiceName(scope, name string) string {
	if scope == "user" {
		return "user:" + name
	}
	return name
}

func resourceSelected(value map[string]any, profile string, selections map[string]bool) bool {
	if profiles, ok := stringSlice(value["profiles"]); ok && len(profiles) > 0 && !containsString(profiles, profile) {
		return false
	}
	if required, ok := stringSlice(value["selections"]); ok {
		for _, selection := range required {
			if !selections[selection] {
				return false
			}
		}
	}
	return true
}

func stringSlice(value any) ([]string, bool) {
	items, ok := value.([]any)
	if !ok {
		return nil, false
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if value, ok := item.(string); ok {
			result = append(result, value)
		}
	}
	return result, true
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func uniqueStrings(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func metadataStrings(value any, key string) []string {
	var result []string
	var walk func(any)
	walk = func(current any) {
		mapping, ok := current.(map[string]any)
		if !ok {
			return
		}
		if values, ok := mapping[key].([]any); ok {
			for _, value := range values {
				if item, ok := value.(string); ok {
					result = append(result, item)
				}
			}
		}
		for _, child := range mapping {
			walk(child)
		}
	}
	walk(value)
	return result
}

func serviceNames(value any, scope string) []string {
	var result []string
	var walk func(any)
	walk = func(current any) {
		mapping, ok := current.(map[string]any)
		if !ok {
			return
		}
		if services, ok := mapping["services"].(map[string]any); ok {
			if values, ok := services[scope].([]any); ok {
				for _, value := range values {
					if item, ok := value.(string); ok {
						result = append(result, item)
					}
				}
			}
		}
		for _, child := range mapping {
			walk(child)
		}
	}
	walk(value)
	return result
}

func (m *Manifest) PackageOrigin(name string) string {
	var sources []string
	var walk func(any)
	walk = func(value any) {
		mapping, ok := value.(map[string]any)
		if !ok {
			return
		}
		if packageValue, found := mapping[name]; found {
			if packageMap, ok := packageValue.(map[string]any); ok {
				if source, ok := packageMap["source"].(string); ok && source != "" {
					sources = append(sources, source)
				}
			}
		}
		for _, child := range mapping {
			walk(child)
		}
	}
	walk(m.Data)
	if len(sources) > 0 {
		sort.Strings(sources)
		return sources[0]
	}
	return m.Source()
}

func (m *Manifest) HasPackage(name string) bool {
	found := false
	var walk func(any)
	walk = func(value any) {
		mapping, ok := value.(map[string]any)
		if !ok || found {
			return
		}
		if _, ok := mapping[name]; ok {
			found = true
			return
		}
		for _, child := range mapping {
			walk(child)
		}
	}
	walk(m.Data)
	return found
}

func (m *Manifest) AddPackage(target, path, name string) error {
	contents, err := os.ReadFile(target)
	if err != nil {
		return fmt.Errorf("read manifest %s: %w", target, err)
	}
	var document yaml.Node
	if err := yaml.Unmarshal(contents, &document); err != nil {
		return fmt.Errorf("parse manifest %s: %w", target, err)
	}
	if len(document.Content) == 0 {
		return errors.New("manifest is empty")
	}
	mapping, err := mappingAt(document.Content[0], splitPath(path), true)
	if err != nil {
		return err
	}
	if mappingValue(mapping, name) != nil {
		return fmt.Errorf("package is already declared: %s", name)
	}
	mapping.Content = append(mapping.Content, scalar(name), &yaml.Node{Kind: yaml.MappingNode})
	sortMapping(mapping)
	output, err := yaml.Marshal(&document)
	if err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	return atomicWrite(target, output)
}

func mappingAt(node *yaml.Node, path []string, create bool) (*yaml.Node, error) {
	if node.Kind == yaml.DocumentNode {
		if len(node.Content) == 0 {
			node.Content = []*yaml.Node{{Kind: yaml.MappingNode}}
		}
		node = node.Content[0]
	}
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("manifest path is not a mapping")
	}
	for _, key := range path {
		value := mappingValue(node, key)
		if value == nil {
			if !create {
				return nil, fmt.Errorf("manifest path does not exist: %s", strings.Join(path, "."))
			}
			value = &yaml.Node{Kind: yaml.MappingNode}
			node.Content = append(node.Content, scalar(key), value)
		}
		if value.Kind == yaml.ScalarNode && value.Tag == "!!null" && create {
			value.Kind = yaml.MappingNode
			value.Tag = ""
			value.Value = ""
		}
		if value.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("manifest path is not a mapping: %s", key)
		}
		node = value
	}
	return node, nil
}

func mappingValue(mapping *yaml.Node, name string) *yaml.Node {
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == name {
			return mapping.Content[index+1]
		}
	}
	return nil
}

func sortMapping(mapping *yaml.Node) {
	type pair struct {
		key, value *yaml.Node
	}
	pairs := make([]pair, 0, len(mapping.Content)/2)
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		pairs = append(pairs, pair{mapping.Content[index], mapping.Content[index+1]})
	}
	sort.SliceStable(pairs, func(left, right int) bool {
		return pairs[left].key.Value < pairs[right].key.Value
	})
	mapping.Content = mapping.Content[:0]
	for _, item := range pairs {
		mapping.Content = append(mapping.Content, item.key, item.value)
	}
}

func scalar(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

func splitPath(path string) []string {
	parts := strings.Split(strings.Trim(path, "."), ".")
	result := parts[:0]
	for _, part := range parts {
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

func atomicWrite(path string, contents []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".dotpkg-*")
	if err != nil {
		return fmt.Errorf("create temporary manifest: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary manifest: %w", err)
	}
	if err := temporary.Chmod(0o644); err != nil {
		temporary.Close()
		return fmt.Errorf("set temporary manifest permissions: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary manifest: %w", err)
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("replace manifest: %w", err)
	}
	return nil
}
