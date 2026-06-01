package clistate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type sourceKind int

const (
	sourceDefault sourceKind = iota
	sourceOverride
	sourceBase
	sourceOverlay
)

// Source identifies where a resolved value came from. Its internals are opaque
// so callers do not need to know how clistate stores base and config.d files.
type Source struct {
	kind  sourceKind
	layer string
	path  string
}

type Resolved[T any] struct {
	Value  T
	Source Source
	Err    error
}

func (s Source) String() string {
	switch s.kind {
	case sourceOverride:
		return "override"
	case sourceBase:
		return "base config"
	case sourceOverlay:
		if s.layer != "" {
			return "overlay " + s.layer
		}
		return "overlay"
	default:
		return "default"
	}
}

func defaultSource() Source  { return Source{kind: sourceDefault} }
func overrideSource() Source { return Source{kind: sourceOverride} }

func (s *Store) baseSource() Source {
	return Source{kind: sourceBase, path: s.path}
}

func overlaySource(layer overlayLayer) Source {
	return Source{kind: sourceOverlay, layer: layer.name, path: layer.path}
}

type overlayLayer struct {
	name string
	path string
	root *node
}

func (s *Store) overlayDir() string {
	base := filepath.Base(s.path)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	if name == "" {
		name = base
	}
	return filepath.Join(filepath.Dir(s.path), name+".d")
}

func normalizeLayerName(layerName string) (string, error) {
	name := strings.TrimSpace(layerName)
	if name == "" {
		return "", fmt.Errorf("overlay layer name is empty")
	}
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") || filepath.Base(name) != name {
		return "", fmt.Errorf("invalid overlay layer name %q", layerName)
	}
	if filepath.Ext(name) == "" {
		name += ".json"
	}
	if filepath.Ext(name) != ".json" || name == ".json" {
		return "", fmt.Errorf("overlay layer name %q must be a JSON file", layerName)
	}
	return name, nil
}

func (s *Store) overlayPath(layerName string) (string, error) {
	name, err := normalizeLayerName(layerName)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.overlayDir(), name), nil
}

func (s *Store) readOverlayLayers() ([]overlayLayer, error) {
	dir := s.overlayDir()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config overlay directory %q: %w", dir, err)
	}

	var names []string
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)

	layers := make([]overlayLayer, 0, len(names))
	for _, name := range names {
		path := filepath.Join(dir, name)
		root, err := readConfigNode(path)
		if err != nil {
			return nil, fmt.Errorf("invalid overlay JSON %q: %w", path, err)
		}
		layers = append(layers, overlayLayer{name: name, path: path, root: root})
	}
	return layers, nil
}

func readConfigNode(path string) (*node, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var root node
	if err := json.Unmarshal(b, &root); err != nil {
		return nil, err
	}
	if _, ok := root.object(); !ok {
		return nil, fmt.Errorf("root value must be a JSON object")
	}
	return &root, nil
}

func writeConfigNode(path string, root *node) error {
	b, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Store) effectiveRootAndLayers() (*node, []overlayLayer, error) {
	s.load()
	root := cloneNode(s.root)
	if root == nil {
		root = newObjectNode()
	}
	layers, err := s.readOverlayLayers()
	if err != nil {
		return nil, nil, err
	}
	for _, layer := range layers {
		root = mergeConfigNodes(root, layer.root)
	}
	return root, layers, nil
}

func (s *Store) sourceForKey(key string, layers []overlayLayer) Source {
	source := defaultSource()
	if _, ok := getFromNode(s.root, key); ok {
		source = s.baseSource()
	}
	for _, layer := range layers {
		if _, ok := getFromNode(layer.root, key); ok {
			source = overlaySource(layer)
		}
	}
	return source
}

func mergeConfigNodes(base, overlay *node) *node {
	if overlay == nil {
		return cloneNode(base)
	}
	baseObj, baseIsObject := valueAsObject(base)
	overlayObj, overlayIsObject := valueAsObject(overlay)
	if !baseIsObject || !overlayIsObject {
		return cloneNode(overlay)
	}

	merged := cloneNode(base)
	mergedObj := merged.ensureObject()
	seen := make(map[string]bool, len(overlayObj))

	for _, key := range overlay.keys {
		if _, ok := overlayObj[key]; !ok || seen[key] {
			continue
		}
		applyOverlayKey(merged, mergedObj, key, baseObj, overlay)
		seen[key] = true
	}

	var fallback []string
	for key := range overlayObj {
		if !seen[key] {
			fallback = append(fallback, key)
		}
	}
	sort.Strings(fallback)
	for _, key := range fallback {
		applyOverlayKey(merged, mergedObj, key, baseObj, overlay)
	}

	return merged
}

func applyOverlayKey(merged *node, mergedObj map[string]any, key string, baseObj map[string]any, overlay *node) {
	overlayChild := overlay.child(key)
	if overlayChild == nil {
		overlayChild = newNodeFromValue(overlay.value.(map[string]any)[key])
	}
	baseChild := merged.child(key)
	if baseChild == nil {
		baseChild = newNodeFromValue(baseObj[key])
	}

	var child *node
	if _, baseObject := valueAsObject(baseChild); baseObject {
		if _, overlayObject := valueAsObject(overlayChild); overlayObject {
			child = mergeConfigNodes(baseChild, overlayChild)
		} else {
			child = cloneNode(overlayChild)
		}
	} else {
		child = cloneNode(overlayChild)
	}

	if _, exists := mergedObj[key]; !exists {
		merged.appendKey(key)
	}
	merged.setChild(key, child)
	mergedObj[key] = child.value
}

func valueAsObject(n *node) (map[string]any, bool) {
	if n == nil {
		return nil, false
	}
	return n.object()
}

func cloneNode(n *node) *node {
	if n == nil {
		return nil
	}
	clone := &node{}
	switch value := n.value.(type) {
	case map[string]any:
		obj := make(map[string]any, len(value))
		clone.value = obj
		clone.keys = append([]string(nil), n.keys...)
		clone.children = make(map[string]*node, len(n.children))
		for key, val := range value {
			child := cloneNode(n.child(key))
			if child == nil {
				child = newNodeFromValue(val)
			}
			clone.children[key] = child
			obj[key] = child.value
		}
	case []any:
		arr := make([]any, len(value))
		clone.value = arr
		for i, val := range value {
			child := cloneNode(itemNode(n, i))
			if child == nil {
				child = newNodeFromValue(val)
			}
			clone.items = append(clone.items, child)
			arr[i] = child.value
		}
	default:
		clone.value = value
	}
	return clone
}
