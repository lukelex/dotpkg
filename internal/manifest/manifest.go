package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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
