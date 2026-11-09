package manifest

import (
	"fmt"
	"regexp"
)

// SelfUpdate configures either a package-managed or GitHub release upgrade.
type SelfUpdate struct {
	Package string
	Origin  string
}

var selfUpdatePackageName = regexp.MustCompile(`^[A-Za-z0-9@._+:-]+$`)

// SelfUpdateSetting uses the host setting when present, otherwise the base
// setting. A present but invalid host setting never falls back to the base.
func SelfUpdateSetting(basePath, hostPath string) (SelfUpdate, error) {
	path := basePath
	if hostPath != "" {
		host, err := readMap(hostPath)
		if err != nil {
			return SelfUpdate{}, err
		}
		if _, ok := host["self_update"]; ok {
			path = hostPath
		}
	}
	contents, err := readMap(path)
	if err != nil {
		return SelfUpdate{}, err
	}
	value, exists := contents["self_update"]
	if !exists {
		return SelfUpdate{}, fmt.Errorf("self-update is disabled: add self_update: {origin: github} or {package: NAME, origin: repo|aur} to manifest %s", path)
	}
	fields, ok := value.(map[string]any)
	if !ok {
		return SelfUpdate{}, fmt.Errorf("%s: self_update must be a mapping", path)
	}
	for key := range fields {
		if key != "package" && key != "origin" {
			return SelfUpdate{}, fmt.Errorf("%s: unsupported self_update field %q", path, key)
		}
	}
	name, _ := fields["package"].(string)
	origin, _ := fields["origin"].(string)
	if origin == "github" {
		if _, exists := fields["package"]; exists {
			return SelfUpdate{}, fmt.Errorf("%s: github self_update must not specify a package", path)
		}
		return SelfUpdate{Origin: origin}, nil
	}
	if !selfUpdatePackageName.MatchString(name) || origin != "repo" && origin != "aur" {
		return SelfUpdate{}, fmt.Errorf("%s: self_update requires a valid package name and origin repo or aur", path)
	}
	return SelfUpdate{Package: name, Origin: origin}, nil
}
