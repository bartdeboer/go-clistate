package clistate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
)

type objectLayout struct {
	keys     []string
	children map[string]*objectLayout
	items    []*objectLayout
}

func newObjectLayout() *objectLayout {
	return &objectLayout{children: make(map[string]*objectLayout)}
}

func ensureObjectLayout(layout *objectLayout) *objectLayout {
	if layout == nil {
		return newObjectLayout()
	}
	if layout.children == nil {
		layout.children = make(map[string]*objectLayout)
	}
	return layout
}

func (o *objectLayout) appendKey(key string) {
	if o == nil {
		return
	}
	for _, existing := range o.keys {
		if existing == key {
			return
		}
	}
	o.keys = append(o.keys, key)
}

func (o *objectLayout) child(key string) *objectLayout {
	if o == nil {
		return nil
	}
	return o.children[key]
}

func (o *objectLayout) setChild(key string, child *objectLayout) {
	if o == nil {
		return
	}
	if o.children == nil {
		o.children = make(map[string]*objectLayout)
	}
	if child == nil {
		delete(o.children, key)
		return
	}
	o.children[key] = child
}

func layoutFromValue(v any) *objectLayout {
	return mergeLayoutForValue(v, nil, nil)
}

func mergeLayoutForValue(v any, existing, incoming *objectLayout) *objectLayout {
	switch t := v.(type) {
	case map[string]any:
		o := newObjectLayout()
		seen := make(map[string]bool, len(t))
		if existing != nil {
			for _, key := range existing.keys {
				if _, ok := t[key]; !ok || seen[key] {
					continue
				}
				o.keys = append(o.keys, key)
				o.setChild(key, mergeLayoutForValue(t[key], existing.child(key), childLayout(incoming, key)))
				seen[key] = true
			}
		}
		if incoming != nil {
			for _, key := range incoming.keys {
				if _, ok := t[key]; !ok || seen[key] {
					continue
				}
				o.keys = append(o.keys, key)
				o.setChild(key, mergeLayoutForValue(t[key], nil, incoming.child(key)))
				seen[key] = true
			}
		}
		var fallback []string
		for key := range t {
			if !seen[key] {
				fallback = append(fallback, key)
			}
		}
		sort.Strings(fallback)
		for _, key := range fallback {
			o.keys = append(o.keys, key)
			o.setChild(key, mergeLayoutForValue(t[key], nil, childLayout(incoming, key)))
		}
		return o
	case []any:
		o := &objectLayout{}
		for i, item := range t {
			o.items = append(o.items, mergeLayoutForValue(item, itemLayout(existing, i), itemLayout(incoming, i)))
		}
		return o
	default:
		return nil
	}
}

func normalizePersistedValue(v any) (any, *objectLayout, error) {
	switch t := v.(type) {
	case nil, bool, string,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return t, nil, nil
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return nil, nil, err
		}
		normalized, layout, err := readJSONValueFromBytes(b)
		return normalized, layout, err
	}
}

func readJSONDocument(b []byte) (map[string]any, *objectLayout, error) {
	v, layout, err := readJSONValueFromBytes(b)
	if err != nil {
		return nil, nil, err
	}
	root, ok := v.(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("root JSON value must be an object")
	}
	if layout == nil {
		layout = newObjectLayout()
	}
	return root, layout, nil
}

func readJSONValueFromBytes(b []byte) (any, *objectLayout, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	v, layout, err := readJSONValue(dec)
	if err != nil {
		return nil, nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("unexpected trailing JSON token")
		}
		return nil, nil, err
	}
	return v, layout, nil
}

func readJSONValue(dec *json.Decoder) (any, *objectLayout, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, nil, err
	}

	if delim, ok := tok.(json.Delim); ok {
		switch delim {
		case '{':
			obj := make(map[string]any)
			layout := newObjectLayout()
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return nil, nil, err
				}
				key, ok := keyTok.(string)
				if !ok {
					return nil, nil, fmt.Errorf("object key is not a string")
				}
				val, child, err := readJSONValue(dec)
				if err != nil {
					return nil, nil, err
				}
				obj[key] = val
				layout.keys = append(layout.keys, key)
				layout.setChild(key, child)
			}
			if _, err := dec.Token(); err != nil {
				return nil, nil, err
			}
			return obj, layout, nil
		case '[':
			var arr []any
			layout := &objectLayout{}
			for dec.More() {
				val, child, err := readJSONValue(dec)
				if err != nil {
					return nil, nil, err
				}
				arr = append(arr, val)
				layout.items = append(layout.items, child)
			}
			if _, err := dec.Token(); err != nil {
				return nil, nil, err
			}
			return arr, layout, nil
		}
	}

	return tok, nil, nil
}

func writeJSONDocument(data map[string]any, layout *objectLayout) ([]byte, error) {
	var buf bytes.Buffer
	if err := writeJSONValue(&buf, data, layout, 0); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeJSONValue(buf *bytes.Buffer, v any, layout *objectLayout, depth int) error {
	switch t := v.(type) {
	case map[string]any:
		return writeJSONObject(buf, t, layout, depth)
	case []any:
		return writeJSONArray(buf, t, layout, depth)
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return err
		}
		normalized, normalizedLayout, err := readJSONValueFromBytes(b)
		if err == nil {
			switch normalized.(type) {
			case map[string]any, []any:
				return writeJSONValue(buf, normalized, normalizedLayout, depth)
			}
		}
		buf.Write(b)
		return nil
	}
}

func writeJSONObject(buf *bytes.Buffer, obj map[string]any, layout *objectLayout, depth int) error {
	buf.WriteByte('{')
	keys := layoutKeys(obj, layout)
	for i, key := range keys {
		if i == 0 {
			buf.WriteByte('\n')
		} else {
			buf.WriteString(",\n")
		}
		writeIndent(buf, depth+1)
		keyJSON, _ := json.Marshal(key)
		buf.Write(keyJSON)
		buf.WriteString(": ")
		if err := writeJSONValue(buf, obj[key], childLayout(layout, key), depth+1); err != nil {
			return err
		}
	}
	if len(keys) > 0 {
		buf.WriteByte('\n')
		writeIndent(buf, depth)
	}
	buf.WriteByte('}')
	return nil
}

func writeJSONArray(buf *bytes.Buffer, arr []any, layout *objectLayout, depth int) error {
	buf.WriteByte('[')
	for i, item := range arr {
		if i == 0 {
			buf.WriteByte('\n')
		} else {
			buf.WriteString(",\n")
		}
		writeIndent(buf, depth+1)
		if err := writeJSONValue(buf, item, itemLayout(layout, i), depth+1); err != nil {
			return err
		}
	}
	if len(arr) > 0 {
		buf.WriteByte('\n')
		writeIndent(buf, depth)
	}
	buf.WriteByte(']')
	return nil
}

func layoutKeys(obj map[string]any, layout *objectLayout) []string {
	seen := make(map[string]bool, len(obj))
	keys := make([]string, 0, len(obj))
	if layout != nil {
		for _, key := range layout.keys {
			if _, ok := obj[key]; ok && !seen[key] {
				keys = append(keys, key)
				seen[key] = true
			}
		}
	}
	var unknown []string
	for key := range obj {
		if !seen[key] {
			unknown = append(unknown, key)
		}
	}
	sort.Strings(unknown)
	return append(keys, unknown...)
}

func childLayout(layout *objectLayout, key string) *objectLayout {
	if layout == nil {
		return nil
	}
	return layout.children[key]
}

func itemLayout(layout *objectLayout, i int) *objectLayout {
	if layout == nil || i < 0 || i >= len(layout.items) {
		return nil
	}
	return layout.items[i]
}

func writeIndent(buf *bytes.Buffer, depth int) {
	for i := 0; i < depth; i++ {
		buf.WriteString("  ")
	}
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
