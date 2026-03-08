package templates_test

import (
	"os"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"
	"gopkg.in/yaml.v3"
)

const gosutoV1SchemaPath = "../../../schemas/gosuto/gosuto-v1.schema.json"

func compileGosutoV1Schema(t *testing.T) *jsonschema.Schema {
	t.Helper()

	schemaBytes, err := os.ReadFile(gosutoV1SchemaPath)
	if err != nil {
		t.Fatalf("read gosuto schema: %v", err)
	}

	compiler := jsonschema.NewCompiler()
	const schemaRef = "gosuto-v1.schema.json"
	if err := compiler.AddResource(schemaRef, strings.NewReader(string(schemaBytes))); err != nil {
		t.Fatalf("add gosuto schema resource: %v", err)
	}

	schema, err := compiler.Compile(schemaRef)
	if err != nil {
		t.Fatalf("compile gosuto schema: %v", err)
	}

	return schema
}

func TestGosutoV1Schema_Compiles(t *testing.T) {
	_ = compileGosutoV1Schema(t)
}

func TestRegistry_Render_CanonicalTemplates_ValidateAgainstGosutoV1Schema(t *testing.T) {
	schema := compileGosutoV1Schema(t)
	reg := newDiskRegistry(t)

	canonicalTemplates := []string{"saito-agent", "kairo-agent", "kumo-agent"}
	for _, templateName := range canonicalTemplates {
		templateName := templateName
		t.Run(templateName, func(t *testing.T) {
			rendered, err := reg.Render(templateName, canonicalVars)
			if err != nil {
				t.Fatalf("Render %s: %v", templateName, err)
			}

			var doc map[string]interface{}
			if err := yaml.Unmarshal(rendered, &doc); err != nil {
				t.Fatalf("yaml.Unmarshal %s: %v", templateName, err)
			}

			if err := schema.Validate(doc); err != nil {
				t.Fatalf("schema.Validate %s: %v", templateName, err)
			}
		})
	}
}
