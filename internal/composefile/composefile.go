// Package composefile reads and edits Compose documents as text. Pinning
// an image rewrites exactly the bytes of one image scalar and nothing
// else, so comments, anchors, key order, and formatting survive an apply
// untouched — the file an operator reviews after a Transaction is the
// file they wrote, one digest longer.
package composefile

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Services returns the document's service names, sorted.
func Services(compose string) ([]string, error) {
	root, err := document(compose)
	if err != nil {
		return nil, err
	}
	services, err := entry(root, "services")
	if err != nil {
		return nil, err
	}
	if services.Kind != yaml.MappingNode {
		return nil, errors.New("compose services must be a mapping")
	}
	names := make([]string, 0, len(services.Content)/2)
	for i := 0; i+1 < len(services.Content); i += 2 {
		names = append(names, services.Content[i].Value)
	}
	slices.Sort(names)
	return names, nil
}

// ServiceImage returns one service's image reference exactly as written.
// A service whose image is absent, non-scalar, or reached through an
// alias has no literal reference and is an error: Ripen only ever pins
// images it can read back verbatim.
func ServiceImage(compose, service string) (string, error) {
	node, err := imageNode(compose, service)
	if err != nil {
		return "", err
	}
	return node.Value, nil
}

// ReplaceServiceImage rewrites one service's image scalar, requiring it
// to still read as expected, and returns the new document text. The
// replacement is written as a double-quoted scalar.
func ReplaceServiceImage(compose, service, expected, replacement string) (string, error) {
	node, err := imageNode(compose, service)
	if err != nil {
		return "", err
	}
	if node.Value != expected {
		return "", errors.New("the target service image changed before replacement")
	}
	if node.Anchor != "" {
		return "", errors.New("an anchored service image cannot be updated safely")
	}
	start, err := offset(compose, node.Line, node.Column)
	if err != nil {
		return "", err
	}
	end, err := scalarEnd(compose, start, node)
	if err != nil {
		return "", err
	}
	return compose[:start] + strconv.Quote(replacement) + compose[end:], nil
}

func imageNode(compose, service string) (*yaml.Node, error) {
	root, err := document(compose)
	if err != nil {
		return nil, err
	}
	services, err := entry(root, "services")
	if err != nil {
		return nil, err
	}
	target, err := entry(services, service)
	if err != nil {
		return nil, err
	}
	image, err := entry(target, "image")
	if err != nil {
		return nil, err
	}
	if image.Kind != yaml.ScalarNode {
		return nil, fmt.Errorf("service %q must have a literal image reference", service)
	}
	return image, nil
}

func document(compose string) (*yaml.Node, error) {
	var parsed yaml.Node
	if err := yaml.Unmarshal([]byte(compose), &parsed); err != nil {
		return nil, fmt.Errorf("compose YAML cannot be parsed safely: %w", err)
	}
	if parsed.Kind != yaml.DocumentNode || len(parsed.Content) == 0 {
		return nil, errors.New("compose document is empty")
	}
	return parsed.Content[0], nil
}

// entry looks up one key of a mapping, refusing aliases: an aliased
// value is shared with somewhere else in the document, so rewriting its
// bytes would silently change that other place too.
func entry(node *yaml.Node, key string) (*yaml.Node, error) {
	if node.Kind == yaml.AliasNode {
		return nil, fmt.Errorf("aliased compose entries cannot be read or updated safely (%q)", key)
	}
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("compose %q must live in a mapping", key)
	}
	var found *yaml.Node
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value != key {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("compose must contain exactly one %q entry", key)
		}
		found = node.Content[i+1]
	}
	if found == nil {
		return nil, fmt.Errorf("compose must contain exactly one %q entry", key)
	}
	if found.Kind == yaml.AliasNode {
		return nil, fmt.Errorf("aliased compose entries cannot be read or updated safely (%q)", key)
	}
	return found, nil
}

// offset converts a 1-based YAML line/column into a byte index. yaml.v3
// reports positions, not spans, so the end is recovered by scalarEnd.
func offset(text string, line, column int) (int, error) {
	index := 0
	for range line - 1 {
		next := strings.IndexByte(text[index:], '\n')
		if next < 0 {
			return 0, errors.New("compose position is outside the document")
		}
		index += next + 1
	}
	index += column - 1
	if index < 0 || index > len(text) {
		return 0, errors.New("compose position is outside the document")
	}
	return index, nil
}

// scalarEnd finds the byte just past a scalar that starts at start.
func scalarEnd(text string, start int, node *yaml.Node) (int, error) {
	switch node.Style {
	case yaml.DoubleQuotedStyle, yaml.SingleQuotedStyle:
		quote := byte('"')
		if node.Style == yaml.SingleQuotedStyle {
			quote = '\''
		}
		if start >= len(text) || text[start] != quote {
			return 0, errors.New("the service image scalar could not be located")
		}
		for i := start + 1; i < len(text); i++ {
			switch text[i] {
			case '\\':
				if quote == '"' {
					i++
				}
			case quote:
				// A doubled quote inside a single-quoted scalar is an
				// escaped quote, not the end of the scalar.
				if quote == '\'' && i+1 < len(text) && text[i+1] == '\'' {
					i++
					continue
				}
				return i + 1, nil
			case '\n':
				return 0, errors.New("a multi-line service image cannot be updated safely")
			}
		}
		return 0, errors.New("the service image scalar is unterminated")
	case 0:
		line := text[start:]
		if cut := strings.IndexByte(line, '\n'); cut >= 0 {
			line = line[:cut]
		}
		trimmed := strings.TrimRight(line, " \t")
		// A plain scalar cannot contain " #": that starts a comment.
		if cut := strings.Index(trimmed, " #"); cut >= 0 {
			trimmed = strings.TrimRight(trimmed[:cut], " \t")
		}
		if trimmed != node.Value {
			return 0, errors.New("the service image scalar could not be located")
		}
		return start + len(trimmed), nil
	default:
		return 0, errors.New("the service image must be a literal scalar")
	}
}
