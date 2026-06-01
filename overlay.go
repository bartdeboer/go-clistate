package clistate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	overlayLayerLevel = 50
	baseLayerLevel    = 100
)

type sourceKind int

const (
	sourceDefault sourceKind = iota
	sourceOverride
	sourceLayer
)

// Source identifies where a resolved value came from. Its internals are opaque
// so callers do not need to know how clistate stores config files.
type Source struct {
	kind  sourceKind
	name  string
	level int
	path  string
}

type Resolved[T any] struct {
	Value  T
	Source Source
	Err    error
}

// Layer is one JSON config source. Higher levels have higher priority.
type Layer struct {
	Name  string
	Level int
	root  *node

	path string
}

func (s Source) String() string {
	switch s.kind {
	case sourceOverride:
		return "override"
	case sourceLayer:
		if s.name != "" {
			return s.name
		}
		return "config layer"
	default:
		return "default"
	}
}

func defaultSource() Source  { return Source{kind: sourceDefault} }
func overrideSource() Source { return Source{kind: sourceOverride, level: 1000} }
func layerSource(layer Layer) Source {
	return Source{kind: sourceLayer, name: layer.Name, level: layer.Level, path: layer.path}
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

func (s *Store) loadLayers() error {
	if s.loaded {
		return nil
	}

	layers, err := s.readLocalLayers()
	if err != nil {
		return err
	}
	s.layers = layers
	s.loaded = true
	return nil
}

func (s *Store) readLocalLayers() ([]Layer, error) {
	var layers []Layer
	overlays, err := s.readOverlayLayers()
	if err != nil {
		return nil, err
	}
	layers = append(layers, overlays...)
	layers = append(layers, s.readBaseLayer())
	return sortLayersLowToHigh(layers), nil
}

func (s *Store) readBaseLayer() Layer {
	root := newObjectNode()
	if parsed, err := readConfigNode(s.path); err == nil {
		root = parsed
	}
	return Layer{Name: filepath.Base(s.path), Level: baseLayerLevel, root: root, path: s.path}
}

func (s *Store) readOverlayLayers() ([]Layer, error) {
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

	layers := make([]Layer, 0, len(names))
	for _, name := range names {
		path := filepath.Join(dir, name)
		root, err := readConfigNode(path)
		if err != nil {
			return nil, fmt.Errorf("invalid overlay JSON %q: %w", path, err)
		}
		layers = append(layers, Layer{Name: name, Level: overlayLayerLevel, root: root, path: path})
	}
	return layers, nil
}

func sortLayersLowToHigh(layers []Layer) []Layer {
	sort.SliceStable(layers, func(i, j int) bool {
		if layers[i].Level != layers[j].Level {
			return layers[i].Level < layers[j].Level
		}
		return layers[i].Name < layers[j].Name
	})
	return layers
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

func (s *Store) winningNode(key string) (*node, Source, bool, error) {
	if err := s.loadLayers(); err != nil {
		return nil, defaultSource(), false, err
	}
	for i := len(s.layers) - 1; i >= 0; i-- {
		layer := s.layers[i]
		if n, ok := findNode(layer.root, key); ok {
			return n, layerSource(layer), true, nil
		}
	}
	return nil, defaultSource(), false, nil
}

func (s *Store) localMergedNode(key string, merged *node, source Source, found bool) (*node, Source, bool, error) {
	if err := s.loadLayers(); err != nil {
		return nil, defaultSource(), false, err
	}
	for _, layer := range s.layers {
		if n, ok := findNode(layer.root, key); ok {
			if found {
				merged = mergeConfigNodes(merged, n)
			} else {
				merged = cloneNode(n)
			}
			source = layerSource(layer)
			found = true
		}
	}
	return merged, source, found, nil
}

func mergeConfigNodes(low, high *node) *node {
	if high == nil {
		return cloneNode(low)
	}
	lowObj, lowIsObject := valueAsObject(low)
	highObj, highIsObject := valueAsObject(high)
	if !lowIsObject || !highIsObject {
		return cloneNode(high)
	}

	merged := cloneNode(low)
	mergedObj := merged.ensureObject()
	seen := make(map[string]bool, len(highObj))

	for _, key := range high.keys {
		if _, ok := highObj[key]; !ok || seen[key] {
			continue
		}
		applyHighKey(merged, mergedObj, key, lowObj, high)
		seen[key] = true
	}

	var fallback []string
	for key := range highObj {
		if !seen[key] {
			fallback = append(fallback, key)
		}
	}
	sort.Strings(fallback)
	for _, key := range fallback {
		applyHighKey(merged, mergedObj, key, lowObj, high)
	}

	return merged
}

func applyHighKey(merged *node, mergedObj map[string]any, key string, lowObj map[string]any, high *node) {
	highChild := high.child(key)
	if highChild == nil {
		highChild = newNodeFromValue(high.value.(map[string]any)[key])
	}
	lowChild := merged.child(key)
	if lowChild == nil {
		lowChild = newNodeFromValue(lowObj[key])
	}

	child := cloneNode(highChild)
	if _, lowObject := valueAsObject(lowChild); lowObject {
		if _, highObject := valueAsObject(highChild); highObject {
			child = mergeConfigNodes(lowChild, highChild)
		}
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
