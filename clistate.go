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
	root   *node
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
		root: newObjectNode(),
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
		root: newObjectNode(),
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
	return s.ResolveString(key, def, override...).Value
}

func (s *Store) GetInt(key string, def int, override ...*int) int {
	return s.ResolveInt(key, def, override...).Value
}

func (s *Store) GetFloat(key string, def float64, override ...*float64) float64 {
	return s.ResolveFloat(key, def, override...).Value
}

func (s *Store) GetBool(key string, def bool, override ...*bool) bool {
	return s.ResolveBool(key, def, override...).Value
}

func (s *Store) ResolveString(key, def string, override ...*string) Resolved[string] {
	if len(override) > 0 && override[0] != nil {
		return Resolved[string]{Value: *override[0], Source: overrideSource()}
	}
	v, source, ok, err := s.resolveValue(key)
	if err != nil {
		return Resolved[string]{Value: def, Source: defaultSource(), Err: err}
	}
	if ok {
		if str, ok := v.(string); ok {
			return Resolved[string]{Value: str, Source: source}
		}
	}
	return Resolved[string]{Value: def, Source: defaultSource()}
}

func (s *Store) ResolveInt(key string, def int, override ...*int) Resolved[int] {
	if len(override) > 0 && override[0] != nil {
		return Resolved[int]{Value: *override[0], Source: overrideSource()}
	}
	v, source, ok, err := s.resolveValue(key)
	if err != nil {
		return Resolved[int]{Value: def, Source: defaultSource(), Err: err}
	}
	if ok {
		switch t := v.(type) {
		case float64:
			return Resolved[int]{Value: int(t), Source: source}
		case float32:
			return Resolved[int]{Value: int(t), Source: source}
		case int:
			return Resolved[int]{Value: t, Source: source}
		}
	}
	return Resolved[int]{Value: def, Source: defaultSource()}
}

func (s *Store) ResolveFloat(key string, def float64, override ...*float64) Resolved[float64] {
	if len(override) > 0 && override[0] != nil {
		return Resolved[float64]{Value: *override[0], Source: overrideSource()}
	}
	v, source, ok, err := s.resolveValue(key)
	if err != nil {
		return Resolved[float64]{Value: def, Source: defaultSource(), Err: err}
	}
	if ok {
		switch t := v.(type) {
		case float64:
			return Resolved[float64]{Value: t, Source: source}
		case float32:
			return Resolved[float64]{Value: float64(t), Source: source}
		case int:
			return Resolved[float64]{Value: float64(t), Source: source}
		}
	}
	return Resolved[float64]{Value: def, Source: defaultSource()}
}

func (s *Store) ResolveBool(key string, def bool, override ...*bool) Resolved[bool] {
	if len(override) > 0 && override[0] != nil {
		return Resolved[bool]{Value: *override[0], Source: overrideSource()}
	}
	v, source, ok, err := s.resolveValue(key)
	if err != nil {
		return Resolved[bool]{Value: def, Source: defaultSource(), Err: err}
	}
	if ok {
		if b, ok := v.(bool); ok {
			return Resolved[bool]{Value: b, Source: source}
		}
	}
	return Resolved[bool]{Value: def, Source: defaultSource()}
}

func (s *Store) GetStruct(key string, out any) bool {
	_, ok, err := s.ResolveStruct(key, out)
	return ok && err == nil
}

func (s *Store) ResolveStruct(key string, out any) (Source, bool, error) {
	v, source, ok, err := s.resolveValue(key)
	if err != nil {
		return defaultSource(), false, err
	}
	if !ok {
		return defaultSource(), false, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return source, false, err
	}
	if err := json.Unmarshal(b, out); err != nil {
		return source, false, err
	}
	return source, true, nil
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

func (s *Store) PersistOverlayString(layerName, key, val string) error {
	return s.persistOverlay(layerName, key, val)
}
func (s *Store) PersistOverlayInt(layerName, key string, val int) error {
	return s.persistOverlay(layerName, key, val)
}
func (s *Store) PersistOverlayFloat(layerName, key string, val float64) error {
	return s.persistOverlay(layerName, key, val)
}
func (s *Store) PersistOverlayBool(layerName, key string, val bool) error {
	return s.persistOverlay(layerName, key, val)
}
func (s *Store) PersistOverlayStruct(layerName, key string, val any) error {
	return s.persistOverlay(layerName, key, val)
}

func (s *Store) UnsetOverlay(layerName, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path, root, err := s.loadOverlayForWrite(layerName)
	if err != nil {
		return err
	}
	if err := unsetInNode(root, key); err != nil {
		return err
	}
	return writeConfigNode(path, root)
}

// -------------- internals --------------

func (s *Store) load() {
	if s.loaded {
		return
	}
	s.loaded = true
	if s.root == nil {
		s.root = newObjectNode()
	}

	b, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var root node
	if err := json.Unmarshal(b, &root); err != nil {
		return
	}
	if _, ok := root.object(); !ok {
		return
	}
	s.root = &root
}

func (s *Store) save() error {
	return writeConfigNode(s.path, s.root)
}

func (s *Store) get(key string) (any, bool) {
	v, _, ok, err := s.resolveValue(key)
	if err == nil && ok {
		return v, true
	}
	return nil, false
}

func (s *Store) resolveValue(key string) (any, Source, bool, error) {
	s.mu.Lock()
	root, layers, err := s.effectiveRootAndLayers()
	if err != nil {
		s.mu.Unlock()
		return nil, defaultSource(), false, err
	}
	if v, ok := getFromNode(root, key); ok {
		source := s.sourceForKey(key, layers)
		s.mu.Unlock()
		return v, source, true, nil
	}
	s.mu.Unlock()

	if s.parent != nil {
		return s.parent.resolveValue(key)
	}
	return nil, defaultSource(), false, nil
}

func (s *Store) persist(key string, val any) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.load()
	valueNode, err := newNodeFromPersistedValue(val)
	if err != nil {
		return err
	}
	if old, ok := getFromNode(s.root, key); ok && jsonEquivalent(old, valueNode.value) {
		return nil
	}
	if err := setInNode(s.root, key, valueNode); err != nil {
		return err
	}
	return s.save()
}

func (s *Store) persistOverlay(layerName, key string, val any) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	valueNode, err := newNodeFromPersistedValue(val)
	if err != nil {
		return err
	}
	path, root, err := s.loadOverlayForWrite(layerName)
	if err != nil {
		return err
	}
	if old, ok := getFromNode(root, key); ok && jsonEquivalent(old, valueNode.value) {
		return nil
	}
	if err := setInNode(root, key, valueNode); err != nil {
		return err
	}
	return writeConfigNode(path, root)
}

func (s *Store) loadOverlayForWrite(layerName string) (string, *node, error) {
	path, err := s.overlayPath(layerName)
	if err != nil {
		return "", nil, err
	}
	root := newObjectNode()
	if _, err := os.Stat(path); err == nil {
		root, err = readConfigNode(path)
		if err != nil {
			return "", nil, fmt.Errorf("read overlay %q: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return "", nil, err
	}
	return path, root, nil
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

// getFromNode traverses nested objects using parsed path segments.
// Field segments are snake-cased. Literal bracket segments are not.
func getFromNode(root *node, key string) (any, bool) {
	segs, err := parsePath(key)
	if err != nil {
		return nil, false
	}
	current := root

	for i, seg := range segs {
		stored := resolveSegmentKey(seg)
		obj, ok := current.object()
		if !ok {
			return nil, false
		}
		v, ok := obj[stored]
		if !ok {
			return nil, false
		}
		if i == len(segs)-1 {
			return v, true
		}
		current = current.child(stored)
		if current == nil {
			current = newNodeFromValue(v)
		}
	}
	return nil, false
}

// setInNode mirrors getFromNode but creates intermediate objects as needed.
func setInNode(root *node, key string, valueNode *node) error {
	segs, err := parsePath(key)
	if err != nil {
		return err
	}
	current := root

	for i, seg := range segs {
		stored := resolveSegmentKey(seg)
		obj := current.ensureObject()
		if i == len(segs)-1 {
			existingChild := current.child(stored)
			if _, ok := obj[stored]; !ok {
				current.appendKey(stored)
			}
			merged := mergeNodes(existingChild, valueNode)
			current.setChild(stored, merged)
			obj[stored] = merged.value
			return nil
		}

		next, ok := obj[stored]
		if !ok {
			child := newObjectNode()
			obj[stored] = child.value
			current.appendKey(stored)
			current.setChild(stored, child)
			current = child
			continue
		}

		child := current.child(stored)
		if child == nil {
			child = newNodeFromValue(next)
			current.setChild(stored, child)
		}
		if _, ok := child.object(); !ok {
			child = newObjectNode()
			obj[stored] = child.value
			current.setChild(stored, child)
			current = child
			continue
		}
		current = child
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
