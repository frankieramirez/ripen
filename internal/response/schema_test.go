package response

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPublishedSchemasMatchTheGeneratedOnes(t *testing.T) {
	directory := filepath.Join("..", "..", "docs", "schema", "v1")
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}

	if update {
		for name := range published(entries) {
			if err := os.Remove(filepath.Join(directory, name)); err != nil {
				t.Fatal(err)
			}
		}
	}
	remaining := published(entries)
	for command, schema := range Schemas() {
		name := command + ".json"
		if !remaining[name] && !update {
			t.Errorf("docs/schema/v1/%s is missing; %s", name, howToRegenerate)
			continue
		}
		delete(remaining, name)

		generated, err := json.MarshalIndent(schema, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		generated = append(generated, '\n')
		if update {
			if err := os.WriteFile(filepath.Join(directory, name), generated, 0o600); err != nil {
				t.Fatal(err)
			}
			continue
		}
		onDisk, err := os.ReadFile(filepath.Join(directory, name)) // #nosec G304 -- test fixture path
		if err != nil {
			t.Fatal(err)
		}
		if string(onDisk) != string(generated) {
			t.Errorf("docs/schema/v1/%s does not match `ripen schema`; %s", name, howToRegenerate)
		}
	}
	for name := range remaining {
		t.Errorf("docs/schema/v1/%s has no command; delete it", name)
	}
}

func published(entries []os.DirEntry) map[string]bool {
	names := map[string]bool{}
	for _, entry := range entries {
		names[entry.Name()] = true
	}
	return names
}

var update = os.Getenv("RIPEN_UPDATE_SCHEMAS") != ""

const howToRegenerate = "regenerate with RIPEN_UPDATE_SCHEMAS=1 go test ./internal/response/"

func TestEveryCommandHasAPublishedSchema(t *testing.T) {
	schemas := Schemas()

	for _, command := range Commands {
		if _, ok := schemas[command]; !ok {
			t.Errorf("command %q has no schema", command)
		}
	}
	if len(schemas) != len(Commands) {
		t.Errorf("schemas = %d, commands = %d: the two lists must agree", len(schemas), len(Commands))
	}
}

func TestAnAbsentValueIsNullRatherThanAnEmptyString(t *testing.T) {
	identity := Identity{Backend: "portainer", Stack: "media", Service: Optional("")}

	encoded, err := json.Marshal(identity)
	if err != nil {
		t.Fatal(err)
	}

	if got, want := string(encoded), `{"backend":"portainer","stack":"media","service":null}`; got != want {
		t.Errorf("encoded = %s, want %s", got, want)
	}
}

func TestAFailureCarriesAClosedCodeAndItsRetryability(t *testing.T) {
	locked := Fail("run", stamp(), CodeStateLocked, "another run holds the lease")
	usage := Fail("run", stamp(), CodeUsage, "unknown flag")

	if locked.OK || locked.Error == nil || !locked.Error.Retryable {
		t.Errorf("locked = %+v, want a retryable failure", locked.Error)
	}
	if usage.Error.Retryable {
		t.Error("a usage error is never retryable: nothing changes by waiting")
	}
	if locked.SchemaVersion != SchemaVersion || locked.Command != "run" {
		t.Errorf("envelope = %+v, want the versioned command envelope", locked)
	}
}

func stamp() time.Time {
	return time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
}
