// Package confdir loads a config directory: every *.yaml file in it, merged in file name order.
package confdir

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"go.yaml.in/yaml/v3"
)

var kindNames = map[yaml.Kind]string{
	yaml.MappingNode:  "mapping",
	yaml.SequenceNode: "list",
	yaml.ScalarNode:   "scalar",
}

// listFiles lists the non-hidden *.yaml files in dir in name order.
func listFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, entry := range entries {
		if name := entry.Name(); filepath.Ext(name) == ".yaml" && !strings.HasPrefix(name, ".") {
			files = append(files, filepath.Join(dir, name))
		}
	}
	return files, nil
}

// Decode merges the files in dir in name order (mappings by key, lists appended, later scalars win) into out.
// Each file is first decoded strictly on its own, so an unknown field or wrong type names the file at fault.
func Decode(kind, dir string, out any) error {
	files, err := listFiles(dir)
	if err != nil {
		return fmt.Errorf("read %s config directory: %w", kind, err)
	}
	var merged *yaml.Node
	for _, file := range files {
		root, err := parseFile(kind, file, reflect.TypeOf(out).Elem())
		if err != nil {
			return err
		}
		switch {
		case root == nil:
		case merged == nil:
			merged = root
		default:
			if err := merge(merged, root, ""); err != nil {
				return fmt.Errorf("merge %s config %s: %w", kind, file, err)
			}
		}
	}
	if merged == nil {
		return nil
	}
	// Every file already passed a strict decode, so the merged tree has no unknown fields to reject.
	return merged.Decode(out)
}

// parseFile checks one file against the schema and returns its top-level mapping with aliases expanded, or nil when empty.
func parseFile(kind, file string, schema reflect.Type) (*yaml.Node, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read %s config: %w", kind, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse %s config %s: %w", kind, file, err)
	}
	if len(doc.Content) == 0 || isNull(doc.Content[0]) {
		return nil, nil
	}
	if doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("parse %s config %s: top level must be a mapping", kind, file)
	}
	if err := decodeStrict(data, reflect.New(schema).Interface()); err != nil {
		return nil, fmt.Errorf("parse %s config %s: %w", kind, file, err)
	}
	return expand(doc.Content[0]), nil
}

func decodeStrict(data []byte, out any) error {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(out); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// merge folds src into dst: mappings merge by key, lists append, scalars are replaced, and null never overrides.
func merge(dst, src *yaml.Node, key string) error {
	if dst.Kind == yaml.AliasNode {
		*dst = *expand(dst.Alias)
	}
	src = expand(src)
	switch {
	case isNull(src):
		return nil
	case isNull(dst):
		*dst = *src
		return nil
	case dst.Kind != src.Kind:
		return fmt.Errorf("%s: cannot merge a %s into a %s", key, kindNames[src.Kind], kindNames[dst.Kind])
	case src.Kind == yaml.SequenceNode:
		dst.Content = append(dst.Content, src.Content...)
	case src.Kind == yaml.MappingNode:
		return mergeMapping(dst, src, key)
	default:
		*dst = *src
	}
	return nil
}

func mergeMapping(dst, src *yaml.Node, key string) error {
	for i := 0; i+1 < len(src.Content); i += 2 {
		name, value := src.Content[i], src.Content[i+1]
		path := name.Value
		if key != "" {
			path = key + "." + name.Value
		}
		j := indexOfKey(dst, name.Value)
		if j < 0 {
			dst.Content = append(dst.Content, name, value)
			continue
		}
		if err := merge(dst.Content[j+1], value, path); err != nil {
			return err
		}
	}
	return nil
}

func indexOfKey(mapping *yaml.Node, name string) int {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == name {
			return i
		}
	}
	return -1
}

func isNull(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.ShortTag() == "!!null"
}

// expand copies n with every alias replaced by its target, so nodes can move between files without dangling anchors.
func expand(n *yaml.Node) *yaml.Node {
	if n.Kind == yaml.AliasNode {
		return expand(n.Alias)
	}
	copied := *n
	copied.Anchor = ""
	copied.Content = make([]*yaml.Node, len(n.Content))
	for i, child := range n.Content {
		copied.Content[i] = expand(child)
	}
	return &copied
}
