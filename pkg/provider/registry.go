package provider

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode"
)

type Registry struct {
	mu        sync.RWMutex
	factories map[string]Factory
}

func NewRegistry() *Registry {
	return &Registry{factories: make(map[string]Factory)}
}

func (r *Registry) Register(factory Factory) error {
	if factory == nil {
		return errors.New("register nil provider factory")
	}
	d := factory.Descriptor()
	if err := validateDescriptor(d); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.factories[d.ID]; exists {
		return fmt.Errorf("provider %q is already registered", d.ID)
	}
	r.factories[d.ID] = factory
	return nil
}

func validateDescriptor(d Descriptor) error {
	if !validID(d.ID) {
		return fmt.Errorf("invalid provider ID %q", d.ID)
	}
	if d.ContractVersion != ContractVersion {
		return fmt.Errorf("provider %q uses contract version %d; FuseDock supports %d", d.ID, d.ContractVersion, ContractVersion)
	}
	if strings.TrimSpace(d.Name) == "" {
		return fmt.Errorf("provider %q has no display name", d.ID)
	}
	keys := make(map[string]bool)
	for _, option := range d.Options {
		if !validKey(option.Key) || strings.TrimSpace(option.Description) == "" {
			return fmt.Errorf("provider %q has an invalid option specification %q", d.ID, option.Key)
		}
		if keys[option.Key] {
			return fmt.Errorf("provider %q repeats option %q", d.ID, option.Key)
		}
		keys[option.Key] = true
	}
	for _, credential := range d.Credentials {
		if !validKey(credential.Key) || strings.TrimSpace(credential.Description) == "" {
			return fmt.Errorf("provider %q has an invalid credential specification %q", d.ID, credential.Key)
		}
		if keys[credential.Key] {
			return fmt.Errorf("provider %q repeats key %q across options and credentials", d.ID, credential.Key)
		}
		keys[credential.Key] = true
	}
	return nil
}

func validKey(key string) bool {
	if key == "" || len(key) > 64 {
		return false
	}
	for _, r := range key {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

func (r *Registry) Get(id string) (Factory, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	factory, ok := r.factories[id]
	return factory, ok
}

func (r *Registry) Descriptors() []Descriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Descriptor, 0, len(r.factories))
	for _, factory := range r.factories {
		result = append(result, factory.Descriptor())
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func validID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

// CleanRelativePath validates a mount-facing path and returns its components.
func CleanRelativePath(path string) ([]string, error) {
	path = strings.ReplaceAll(path, "\\", "/")
	path = strings.Trim(path, "/")
	if path == "" || path == "." {
		return nil, nil
	}
	parts := strings.Split(path, "/")
	for _, part := range parts {
		if !ValidName(part) {
			return nil, ErrInvalidName
		}
	}
	return parts, nil
}

// ValidName applies the common cross-platform namespace constraints. Windows
// adapters may add their reserved-name checks when they are introduced.
func ValidName(name string) bool {
	return name != "" && name != "." && name != ".." && len(name) <= 255 &&
		!strings.ContainsAny(name, "/\\\x00") && strings.IndexFunc(name, unicode.IsControl) < 0
}
