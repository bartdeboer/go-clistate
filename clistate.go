package clistate

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/bartdeboer/words"
)

type Store struct {
	path   string
	data   map[string]any
	loaded bool

	parent *Store
	flags  *flag.FlagSet

	mu sync.Mutex
}

// NewGlobal creates a global store at ~/.<app>/<name>.json
// e.g. NewGlobal("vix", "state") -> ~/.vix/state.json
func NewGlobal(app, name string) (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, "."+app)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{
		path: filepath.Join(dir, name+".json"),
		data: make(map[string]any),
	}, nil
}

// NewCwd creates a cwd-local store at ./.<app>/<name>.json
// and automatically wires in a global store ~/.<app>/<name>.json
// as a parent fallback.
func NewCwd(app, name string) (*Store, error) {
	dir := filepath.Join(".", "."+app)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	s := &Store{
		path: filepath.Join(dir, name+".json"),
		data: make(map[string]any),
	}

	// Try to attach matching global as parent, but don't fail if it breaks.
	if g, err := NewGlobal(app, name); err == nil {
		s.parent = g
	}

	return s, nil
}

// SetFlags stores the flagset for future integrations (e.g. auto-CLI wiring).
func (s *Store) SetFlags(fs *flag.FlagSet) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flags = fs
}

// -------------- public getters ----------------

func (s *Store) Get(key string, fallback any) any {
	if v, ok := s.get(key); ok {
		return v
	}
	return fallback
}

func (s *Store) GetString(key, def string, override ...*string) string {
	if len(override) > 0 && override[0] != nil && *override[0] != "" {
		return *override[0]
	}
	if v, ok := s.get(key); ok {
		if str, ok := v.(string); ok && str != "" {
			return str
		}
	}
	return def
}

func (s *Store) GetInt(key string, def int, override ...*int) int {
	if len(override) > 0 && override[0] != nil && *override[0] != 0 {
		return *override[0]
	}
	if v, ok := s.get(key); ok {
		switch t := v.(type) {
		case float64:
			if int(t) != 0 {
				return int(t)
			}
		case float32:
			if int(t) != 0 {
				return int(t)
			}
		case int:
			if t != 0 {
				return t
			}
		}
	}
	return def
}

func (s *Store) GetFloat(key string, def float64, override ...*float64) float64 {
	if len(override) > 0 && override[0] != nil && *override[0] != 0 {
		return *override[0]
	}
	if v, ok := s.get(key); ok {
		switch t := v.(type) {
		case float64:
			if t != 0 {
				return t
			}
		case float32:
			if t != 0 {
				return float64(t)
			}
		case int:
			if t != 0 {
				return float64(t)
			}
		}
	}
	return def
}

func (s *Store) GetBool(key string, def bool, override ...*bool) bool {
	if len(override) > 0 && override[0] != nil {
		return *override[0]
	}
	if v, ok := s.get(key); ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return def
}

// -------------- public persistence --------------

func (s *Store) PersistString(key, val string) error {
	return s.persist(key, val)
}
func (s *Store) PersistInt(key string, val int) error {
	return s.persist(key, val)
}
func (s *Store) PersistFloat(key string, val float64) error {
	return s.persist(key, val)
}
func (s *Store) PersistBool(key string, val bool) error {
	return s.persist(key, val)
}

// -------------- internals --------------

func (s *Store) load() {
	if s.loaded {
		return
	}
	s.loaded = true

	b, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	_ = json.Unmarshal(b, &s.data)
}

func (s *Store) save() error {
	tmp := s.path + ".tmp"

	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) get(key string) (any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.load()
	if v, ok := getFromMap(s.data, key); ok {
		return v, true
	}
	if s.parent != nil {
		s.parent.mu.Lock()
		defer s.parent.mu.Unlock()
		s.parent.load()
		return getFromMap(s.parent.data, key)
	}
	return nil, false
}

func (s *Store) persist(key string, val any) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.load()
	setInMap(s.data, key, val)
	return s.save()
}

// getFromMap traverses nested maps using dot-separated keys,
// converting each segment to snake_case for JSON storage.
func getFromMap(root map[string]any, key string) (any, bool) {
	parts := strings.Split(key, ".")
	m := any(root)

	for i, p := range parts {
		snake := words.ToSnakeCase(p)

		asMap, ok := m.(map[string]any)
		if !ok {
			return nil, false
		}
		v, ok := asMap[snake]
		if !ok {
			return nil, false
		}
		if i == len(parts)-1 {
			return v, true
		}
		m = v
	}
	return nil, false
}

// setInMap mirrors getFromMap but creates intermediate maps as needed.
func setInMap(root map[string]any, key string, val any) {
	parts := strings.Split(key, ".")
	m := root

	for i, p := range parts {
		snake := words.ToSnakeCase(p)
		if i == len(parts)-1 {
			m[snake] = val
			return
		}
		next, ok := m[snake]
		if !ok {
			child := map[string]any{}
			m[snake] = child
			m = child
			continue
		}
		asMap, ok := next.(map[string]any)
		if !ok {
			child := map[string]any{}
			m[snake] = child
			m = child
			continue
		}
		m = asMap
	}
}
