package clistate

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/bartdeboer/words"
)

type Store struct {
	app    string
	path   string
	data   map[string]any
	loaded bool

	parent *Store
	flags  *flag.FlagSet

	mu sync.Mutex
}

type segmentKind int

const (
	segmentField segmentKind = iota
	segmentLiteral
)

type pathSegment struct {
	kind  segmentKind
	value string
}

func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func (s *Store) ParentPath() string {
	if s == nil || s.parent == nil {
		return ""
	}
	return s.parent.path
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
		app:  app,
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
		app:  app,
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

func (s *Store) GetStruct(key string, out any) bool {
	v, ok := s.get(key)
	if !ok {
		return false
	}
	b, err := json.Marshal(v)
	if err != nil {
		return false
	}
	return json.Unmarshal(b, out) == nil
}

func (s *Store) GetProjectDir() string {
	if s == nil {
		return ""
	}

	app := strings.TrimSpace(s.app)
	if app == "" {
		return ""
	}

	projectDir, err := func() (string, error) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}

		cwd, err = filepath.Abs(cwd)
		if err != nil {
			return "", err
		}

		cmd := exec.CommandContext(context.Background(), "go", "list", "-m", "-json")
		cmd.Dir = cwd

		out, err := cmd.Output()
		if err != nil {
			return "", err
		}

		var mod struct {
			Path string
			Dir  string
		}
		if err := json.Unmarshal(out, &mod); err != nil {
			return "", err
		}

		moduleName := filepath.Base(strings.TrimSpace(mod.Path))
		if moduleName != app && strings.TrimPrefix(moduleName, "go-") != app {
			return "", fmt.Errorf("module %q does not match app %q", moduleName, app)
		}

		projectDir := strings.TrimSpace(mod.Dir)
		if projectDir == "" {
			return "", fmt.Errorf("module directory is empty")
		}

		projectDir, err = filepath.Abs(projectDir)
		if err != nil {
			return "", err
		}

		rel, err := filepath.Rel(projectDir, cwd)
		if err != nil {
			return "", err
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("cwd %q is outside project %q", cwd, projectDir)
		}

		return projectDir, nil
	}()

	if err == nil && projectDir != "" {
		_ = s.PersistString("project_dir", projectDir)
		return projectDir
	}

	return strings.TrimSpace(s.GetString("project_dir", ""))
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
func (s *Store) PersistStruct(key string, val any) error {
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
	if err := setInMap(s.data, key, val); err != nil {
		return err
	}
	return s.save()
}

func normalizeFieldName(v string) string {
	return words.ToSnakeCase(v)
}

func resolveSegmentKey(seg pathSegment) string {
	if seg.kind == segmentLiteral {
		return seg.value
	}
	return normalizeFieldName(seg.value)
}

// parsePath parses paths like:
//
//	profile.name
//	chats["abc123"].providerChatID
//	chats['abc123'].provider_chat_id
//
// Rules:
//   - dot segments are field names and will be snake-cased for storage
//   - bracket segments are literal keys and are never normalized
//   - bracket keys must be quoted with ' or "
func parsePath(path string) ([]pathSegment, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("empty path")
	}

	var segs []pathSegment
	i := 0

	readField := func(start int) (string, int, error) {
		j := start
		for j < len(path) && path[j] != '.' && path[j] != '[' {
			if path[j] == ']' {
				return "", j, fmt.Errorf("unexpected ] at position %d", j)
			}
			j++
		}
		field := strings.TrimSpace(path[start:j])
		if field == "" {
			return "", j, fmt.Errorf("empty field segment at position %d", start)
		}
		return field, j, nil
	}

	readBracket := func(start int) (string, int, error) {
		if start+1 >= len(path) {
			return "", start, fmt.Errorf("unterminated bracket at position %d", start)
		}

		quote := path[start+1]
		if quote != '"' && quote != '\'' {
			return "", start, fmt.Errorf("bracket key must start with quoted string at position %d", start)
		}

		var b strings.Builder
		j := start + 2

		for j < len(path) {
			ch := path[j]
			if ch == '\\' {
				if j+1 >= len(path) {
					return "", j, fmt.Errorf("dangling escape in bracket key at position %d", j)
				}
				b.WriteByte(path[j+1])
				j += 2
				continue
			}
			if ch == quote {
				j++
				if j >= len(path) || path[j] != ']' {
					return "", j, fmt.Errorf("expected ] after quoted bracket key at position %d", j)
				}
				return b.String(), j + 1, nil
			}
			b.WriteByte(ch)
			j++
		}

		return "", j, fmt.Errorf("unterminated quoted bracket key starting at position %d", start)
	}

	for i < len(path) {
		switch path[i] {
		case '.':
			return nil, fmt.Errorf("unexpected dot at position %d", i)
		case '[':
			lit, next, err := readBracket(i)
			if err != nil {
				return nil, err
			}
			segs = append(segs, pathSegment{kind: segmentLiteral, value: lit})
			i = next
		default:
			field, next, err := readField(i)
			if err != nil {
				return nil, err
			}
			segs = append(segs, pathSegment{kind: segmentField, value: field})
			i = next
		}

		if i < len(path) {
			switch path[i] {
			case '.':
				i++
				if i >= len(path) {
					return nil, fmt.Errorf("path cannot end with dot")
				}
			case '[':
				// immediate bracket continuation is allowed, e.g. chats["abc"]
			default:
				return nil, fmt.Errorf("unexpected character %q at position %d", path[i], i)
			}
		}
	}

	if len(segs) == 0 {
		return nil, fmt.Errorf("empty path")
	}

	return segs, nil
}

// getFromMap traverses nested maps using parsed path segments.
// Field segments are snake-cased. Literal bracket segments are not.
func getFromMap(root map[string]any, key string) (any, bool) {
	segs, err := parsePath(key)
	if err != nil {
		return nil, false
	}
	m := any(root)

	for i, seg := range segs {
		stored := resolveSegmentKey(seg)

		asMap, ok := m.(map[string]any)
		if !ok {
			return nil, false
		}
		v, ok := asMap[stored]
		if !ok {
			return nil, false
		}
		if i == len(segs)-1 {
			return v, true
		}
		m = v
	}
	return nil, false
}

// setInMap mirrors getFromMap but creates intermediate maps as needed.
func setInMap(root map[string]any, key string, val any) error {
	segs, err := parsePath(key)
	if err != nil {
		return err
	}
	m := root

	for i, seg := range segs {
		stored := resolveSegmentKey(seg)
		if i == len(segs)-1 {
			m[stored] = val
			return nil
		}
		next, ok := m[stored]
		if !ok {
			child := map[string]any{}
			m[stored] = child
			m = child
			continue
		}
		asMap, ok := next.(map[string]any)
		if !ok {
			child := map[string]any{}
			m[stored] = child
			m = child
			continue
		}
		m = asMap
	}

	return nil
}

// GetTyped returns the zero value of T if the key is missing or cannot be decoded into T.
func GetTyped[T any](s *Store, key string) T {
	v, ok := GetTypedOK[T](s, key)
	if !ok {
		var zero T
		return zero
	}
	return v
}

// GetTypedOK returns (value, true) when found and decoded successfully.
// If missing or decode fails, returns (zero, false).
func GetTypedOK[T any](s *Store, key string) (T, bool) {
	var zero T
	v, ok := s.get(key)
	if !ok {
		return zero, false
	}

	b, err := json.Marshal(v)
	if err != nil {
		return zero, false
	}

	var out T
	if err := json.Unmarshal(b, &out); err != nil {
		return zero, false
	}
	return out, true
}

// PersistTyped persists val under key.
// This stores the concrete type in-memory, and JSON encoding will serialize it correctly.
func PersistTyped[T any](s *Store, key string, val T) error {
	return s.persist(key, val)
}
