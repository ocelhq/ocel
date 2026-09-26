package projectconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"slices"

	"gopkg.in/yaml.v3"

	"github.com/ocelhq/ocel/pkg/configdoc"
)

func readYAML(_ context.Context, configPath string) ([]byte, error) {
	read, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", configPath, err)
	}
	standard, err := yamlToJSON(read)
	if err != nil {
		return nil, fmt.Errorf("%s %w", configPath, err)
	}
	return standard, nil
}

func yamlToJSON(source []byte) ([]byte, error) {
	document, err := onlyDocument(source)
	if err != nil {
		return nil, err
	}
	if document == nil {
		return []byte("null"), nil
	}

	keepSourceText(document, map[*yaml.Node]bool{})
	var tree any
	if err := document.Decode(&tree); err != nil {
		return nil, fmt.Errorf("is not valid YAML: %w", err)
	}
	if err := checkJSONable("", tree); err != nil {
		return nil, err
	}
	return json.Marshal(tree)
}

func onlyDocument(source []byte) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(source))
	var found *yaml.Node
	for {
		var document yaml.Node
		err := decoder.Decode(&document)
		if errors.Is(err, io.EOF) {
			return found, nil
		}
		if err != nil {
			return nil, fmt.Errorf("is not valid YAML: %w", err)
		}
		if isEmpty(&document) {
			continue
		}
		if found != nil {
			return nil, errors.New("contains more than one YAML document, and a config is one document")
		}
		found = &document
	}
}

func isEmpty(document *yaml.Node) bool {
	if len(document.Content) == 0 {
		return true
	}
	root := document.Content[0]
	return root.Kind == yaml.ScalarNode && root.ShortTag() == "!!null"
}

func keepSourceText(node *yaml.Node, seen map[*yaml.Node]bool) {
	if seen[node] {
		return
	}
	seen[node] = true
	switch node.Kind {
	case yaml.ScalarNode:
		switch node.ShortTag() {
		case "!!timestamp", "!!binary":
			node.Tag = "!!str"
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind == yaml.ScalarNode && key.ShortTag() != "!!merge" {
				key.Tag = "!!str"
			}
		}
	case yaml.AliasNode:
		keepSourceText(node.Alias, seen)
		return
	}
	for _, child := range node.Content {
		keepSourceText(child, seen)
	}
}

func checkJSONable(path string, value any) error {
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range slices.Sorted(maps.Keys(typed)) {
			if err := checkJSONable(configdoc.JoinPath(path, key), typed[key]); err != nil {
				return err
			}
		}
	case map[any]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, fmt.Sprint(key))
		}
		slices.Sort(keys)
		return fmt.Errorf("has the key %s under %s, and a config key must be a string — quote it", keys[0], configdoc.PathName(path))
	case []any:
		for i, item := range typed {
			if err := checkJSONable(configdoc.IndexPath(path, i), item); err != nil {
				return err
			}
		}
	case float64:
		if math.IsInf(typed, 0) || math.IsNaN(typed) {
			return fmt.Errorf("sets %s to %v, which a config cannot represent", configdoc.PathName(path), typed)
		}
	}
	return nil
}
