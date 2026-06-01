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
	sourceComposite
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
	case sourceComposite:
		return "composite"
	default:
		return "default"
	}
}

func defaultSource() Source  { return Source{kind: sourceDefault} }
func overrideSource() Source { return Source{kind: sourceOverride, level: 1000} }
func layerSource(layer Layer) Source {
	return Source{kind: sourceLayer, name: layer.Name, level: layer.Level, path: layer.path}
}

func compositeSource() Source { return Source{kind: sourceComposite} }

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
	root, err := s.effectiveRootFromLoadedLayers()
	if err != nil {
		return err
	}
	s.root = root
	s.loaded = true
	return nil
}

func (s *Store) effectiveRootFromLoadedLayers() (*node, error) {
	root := newObjectNode()
	if s.parent != nil {
		s.parent.mu.Lock()
		err := s.parent.loadLayers()
		if err == nil {
			root = cloneNode(s.parent.root)
		}
		s.parent.mu.Unlock()
		if err != nil {
			return nil, err
		}
	}
	for _, layer := range s.layers {
		root = mergeConfigNodes(root, layer.root)
	}
	return root, nil
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
	layer := Layer{Name: filepath.Base(s.path), Level: baseLayerLevel, root: root, path: s.path}
	setSource(root, layerSource(layer))
	return layer
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
		layer := Layer{Name: name, Level: overlayLayerLevel, root: root, path: path}
		setSource(root, layerSource(layer))
		layers = append(layers, layer)
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
	if n, ok := findNode(s.root, key); ok {
		return n, n.source, true, nil
	}
	return nil, defaultSource(), false, nil
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
	merged.source = mergeObjectSource(low.source, high.source)
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

func mergeObjectSource(low, high Source) Source {
	if low.kind == sourceDefault {
		return high
	}
	if high.kind == sourceDefault || low == high {
		return low
	}
	return compositeSource()
}

func setSource(n *node, source Source) {
	if n == nil {
		return
	}
	n.source = source
	for _, child := range n.children {
		setSource(child, source)
	}
	for _, item := range n.items {
		setSource(item, source)
	}
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
	clone.source = n.source
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
