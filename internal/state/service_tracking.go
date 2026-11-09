// Package state persists the selection and ownership of managed resources.
package state

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// ServiceTracking records the state of a managed service.
type ServiceTracking struct {
	Applied  map[string]string `yaml:"applied"`
	Packages []string          `yaml:"packages,omitempty"`
}

// ServiceTracking returns the recorded state of managed services.
func (s *State) ServiceTracking() (map[string]ServiceTracking, error) {
	result := map[string]ServiceTracking{}
	value, exists := s.Data["service_tracking"]
	if !exists {
		return result, nil
	}
	data, err := yaml.Marshal(value)
	if err == nil {
		err = yaml.Unmarshal(data, &result)
	}
	if err != nil {
		return nil, fmt.Errorf("read service tracking: %w", err)
	}
	if result == nil {
		result = map[string]ServiceTracking{}
	}
	return result, nil
}

// SetServiceTracking updates the recorded state of managed services.
func (s *State) SetServiceTracking(value map[string]ServiceTracking) {
	// Store ordinary YAML values so snapshots match freshly loaded state.
	data, _ := yaml.Marshal(value)
	var mapping map[string]any
	_ = yaml.Unmarshal(data, &mapping)
	s.Data["service_tracking"] = mapping
}
