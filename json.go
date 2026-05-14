package clistate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
)

type node struct {
	value    any
	keys     []string
	children map[string]*node
	items    []*node
}

func newObjectNode() *node {
	return &node{
		value:    map[string]any{},
		children: make(map[string]*node),
	}
}

func newNodeFromPersistedValue(v any) (*node, error) {
	switch t := v.(type) {
	case nil, bool, string,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return &node{value: t}, nil
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return nil, err
		}
		var n node
		if err := json.Unmarshal(b, &n); err != nil {
			return nil, err
		}
		return &n, nil
	}
}

func newNodeFromValue(v any) *node {
	switch t := v.(type) {
	case map[string]any:
		n := newObjectNode()
		n.value = t
		for _, key := range sortedKeys(t) {
			n.appendKey(key)
			n.setChild(key, newNodeFromValue(t[key]))
		}
		return n
	case []any:
		n := &node{value: t}
		for _, item := range t {
			n.items = append(n.items, newNodeFromValue(item))
		}
		return n
	default:
		return &node{value: v}
	}
}

func (n *node) UnmarshalJSON(b []byte) error {
	parsed, err := readNodeFromBytes(b)
	if err != nil {
		return err
	}
	*n = *parsed
	return nil
}

func (n node) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	if err := n.writeJSON(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func readNodeFromBytes(b []byte) (*node, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	n, err := readNode(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("unexpected trailing JSON token")
		}
		return nil, err
	}
	return n, nil
}

func readNode(dec *json.Decoder) (*node, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}

	delim, ok := tok.(json.Delim)
	if !ok {
		return &node{value: tok}, nil
	}

	switch delim {
	case '{':
		obj := make(map[string]any)
		n := &node{value: obj, children: make(map[string]*node)}
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyTok.(string)
			if !ok {
				return nil, fmt.Errorf("object key is not a string")
			}
			child, err := readNode(dec)
			if err != nil {
				return nil, err
			}
			if _, seen := obj[key]; !seen {
				n.keys = append(n.keys, key)
			}
			obj[key] = child.value
			n.children[key] = child
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		return n, nil
	case '[':
		var arr []any
		n := &node{value: arr}
		for dec.More() {
			item, err := readNode(dec)
			if err != nil {
				return nil, err
			}
			arr = append(arr, item.value)
			n.items = append(n.items, item)
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		n.value = arr
		return n, nil
	default:
		return nil, fmt.Errorf("unexpected JSON delimiter %q", delim)
	}
}

func (n *node) object() (map[string]any, bool) {
	if n == nil {
		return nil, false
	}
	obj, ok := n.value.(map[string]any)
	return obj, ok
}

func (n *node) ensureObject() map[string]any {
	if obj, ok := n.object(); ok {
		if n.children == nil {
			n.children = make(map[string]*node)
		}
		return obj
	}
	obj := map[string]any{}
	n.value = obj
	n.keys = nil
	n.children = make(map[string]*node)
	n.items = nil
	return obj
}

func (n *node) appendKey(key string) {
	if n == nil {
		return
	}
	for _, existing := range n.keys {
		if existing == key {
			return
		}
	}
	n.keys = append(n.keys, key)
}

func (n *node) child(key string) *node {
	if n == nil {
		return nil
	}
	return n.children[key]
}

func (n *node) setChild(key string, child *node) {
	if n == nil {
		return
	}
	if n.children == nil {
		n.children = make(map[string]*node)
	}
	if child == nil {
		delete(n.children, key)
		return
	}
	n.children[key] = child
}

func mergeNodes(existing, incoming *node) *node {
	if incoming == nil {
		return nil
	}

	incomingObject, ok := incoming.value.(map[string]any)
	if ok {
		merged := &node{value: incomingObject, children: make(map[string]*node)}
		seen := make(map[string]bool, len(incomingObject))

		if existing != nil {
			for _, key := range existing.keys {
				if _, ok := incomingObject[key]; !ok || seen[key] {
					continue
				}
				merged.keys = append(merged.keys, key)
				merged.setChild(key, mergeNodes(existing.child(key), incoming.child(key)))
				seen[key] = true
			}
		}

		for _, key := range incoming.keys {
			if _, ok := incomingObject[key]; !ok || seen[key] {
				continue
			}
			merged.keys = append(merged.keys, key)
			merged.setChild(key, mergeNodes(nil, incoming.child(key)))
			seen[key] = true
		}

		var fallback []string
		for key := range incomingObject {
			if !seen[key] {
				fallback = append(fallback, key)
			}
		}
		sort.Strings(fallback)
		for _, key := range fallback {
			merged.keys = append(merged.keys, key)
			merged.setChild(key, mergeNodes(nil, incoming.child(key)))
		}
		return merged
	}

	incomingArray, ok := incoming.value.([]any)
	if ok {
		merged := &node{value: incomingArray}
		for i := range incomingArray {
			merged.items = append(merged.items, mergeNodes(itemNode(existing, i), itemNode(incoming, i)))
		}
		return merged
	}

	return incoming
}

func itemNode(n *node, i int) *node {
	if n == nil || i < 0 || i >= len(n.items) {
		return nil
	}
	return n.items[i]
}

func (n *node) writeJSON(buf *bytes.Buffer) error {
	if n == nil {
		buf.WriteString("null")
		return nil
	}

	switch value := n.value.(type) {
	case map[string]any:
		return n.writeObject(buf, value)
	case []any:
		return n.writeArray(buf, value)
	default:
		b, err := json.Marshal(value)
		if err != nil {
			return err
		}
		buf.Write(b)
		return nil
	}
}

func (n *node) writeObject(buf *bytes.Buffer, obj map[string]any) error {
	buf.WriteByte('{')
	keys := n.keysFor(obj)
	for i, key := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		keyJSON, _ := json.Marshal(key)
		buf.Write(keyJSON)
		buf.WriteByte(':')
		child := n.child(key)
		if child == nil {
			child = newNodeFromValue(obj[key])
		}
		if err := child.writeJSON(buf); err != nil {
			return err
		}
	}
	buf.WriteByte('}')
	return nil
}

func (n *node) writeArray(buf *bytes.Buffer, arr []any) error {
	buf.WriteByte('[')
	for i, item := range arr {
		if i > 0 {
			buf.WriteByte(',')
		}
		child := itemNode(n, i)
		if child == nil {
			child = newNodeFromValue(item)
		}
		if err := child.writeJSON(buf); err != nil {
			return err
		}
	}
	buf.WriteByte(']')
	return nil
}

func (n *node) keysFor(obj map[string]any) []string {
	seen := make(map[string]bool, len(obj))
	keys := make([]string, 0, len(obj))
	for _, key := range n.keys {
		if _, ok := obj[key]; ok && !seen[key] {
			keys = append(keys, key)
			seen[key] = true
		}
	}
	var fallback []string
	for key := range obj {
		if !seen[key] {
			fallback = append(fallback, key)
		}
	}
	sort.Strings(fallback)
	return append(keys, fallback...)
}

func sortedKeys(obj map[string]any) []string {
	keys := make([]string, 0, len(obj))
	for key := range obj {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func jsonEquivalent(a, b any) bool {
	var canonicalA, canonicalB any
	encodedA, errA := json.Marshal(a)
	encodedB, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return reflect.DeepEqual(a, b)
	}
	if err := json.Unmarshal(encodedA, &canonicalA); err != nil {
		return reflect.DeepEqual(a, b)
	}
	if err := json.Unmarshal(encodedB, &canonicalB); err != nil {
		return reflect.DeepEqual(a, b)
	}
	return reflect.DeepEqual(canonicalA, canonicalB)
}
