package response

import (
	"reflect"
	"slices"
	"strings"
)

// dialect is the JSON Schema dialect the published schemas declare.
const dialect = "https://json-schema.org/draft/2020-12/schema"

// Commands is every command with a published response schema, in the
// order the documentation lists them.
var Commands = []string{
	"status", "candidates", "audit", "explain",
	"run", "propose", "clear-proposal", "clear-breaker",
	"schema", "version",
}

// payloads maps each command to the Go type of its data payload. The
// schemas are generated from these types rather than hand-written, so
// the published schema cannot drift from what the code emits.
func payloads() map[string]reflect.Type {
	return map[string]reflect.Type{
		"status":         reflect.TypeOf(Status{}),
		"candidates":     reflect.TypeOf(Candidates{}),
		"audit":          reflect.TypeOf(Audit{}),
		"explain":        reflect.TypeOf(Explain{}),
		"run":            reflect.TypeOf(Run{}),
		"propose":        reflect.TypeOf(Proposed{}),
		"clear-proposal": reflect.TypeOf(Acknowledged{}),
		"clear-breaker":  reflect.TypeOf(Acknowledged{}),
		"schema":         reflect.TypeOf(SchemaSet{}),
		"version":        reflect.TypeOf(Version{}),
	}
}

// Schemas returns one JSON Schema per command: the Response envelope
// with that command's payload in place of data.
func Schemas() map[string]any {
	types := payloads()
	schemas := make(map[string]any, len(types))
	for command, payload := range types {
		schemas[command] = envelopeSchema(command, payload)
	}
	return schemas
}

func envelopeSchema(command string, payload reflect.Type) map[string]any {
	return map[string]any{
		"$schema":              dialect,
		"title":                "ripen " + command + " response",
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"schema_version", "command", "occurred_at", "ok"},
		"properties": map[string]any{
			"schema_version": map[string]any{"type": "integer", "const": SchemaVersion},
			"command":        map[string]any{"type": "string", "const": command},
			"occurred_at":    map[string]any{"type": "string", "format": "date-time"},
			"ok":             map[string]any{"type": "boolean"},
			"data":           schemaFor(payload),
			"error":          schemaFor(reflect.TypeOf(Error{})),
		},
	}
}

func schemaFor(kind reflect.Type) map[string]any {
	switch kind.Kind() {
	case reflect.Pointer:
		return nullable(schemaFor(kind.Elem()))
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice, reflect.Array:
		return nullable(map[string]any{"type": "array", "items": schemaFor(kind.Elem())})
	case reflect.Map:
		return map[string]any{"type": "object"}
	case reflect.Interface:
		// An unconstrained value: `data` on the envelope itself.
		return map[string]any{}
	case reflect.Struct:
		return structSchema(kind)
	default:
		return map[string]any{}
	}
}

func structSchema(kind reflect.Type) map[string]any {
	properties := map[string]any{}
	var required []any
	collectFields(kind, properties, &required)
	slices.SortFunc(required, func(a, b any) int {
		return strings.Compare(a.(string), b.(string))
	})
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           properties,
		"required":             required,
	}
}

// collectFields walks a struct's JSON shape, flattening embedded structs
// the way encoding/json does.
func collectFields(kind reflect.Type, properties map[string]any, required *[]any) {
	for index := range kind.NumField() {
		field := kind.Field(index)
		if !field.IsExported() {
			continue
		}
		tag := field.Tag.Get("json")
		name, options, _ := strings.Cut(tag, ",")
		if name == "-" {
			continue
		}
		if field.Anonymous && name == "" {
			collectFields(field.Type, properties, required)
			continue
		}
		if name == "" {
			name = field.Name
		}
		properties[name] = schemaFor(field.Type)
		if !strings.Contains(options, "omitempty") {
			*required = append(*required, name)
		}
	}
}

// nullable widens a schema's type to allow null, which is how every
// absent value is rendered.
func nullable(schema map[string]any) map[string]any {
	existing, ok := schema["type"].(string)
	if !ok {
		return schema
	}
	schema["type"] = []any{existing, "null"}
	return schema
}
