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

func TestGosutoV1Schema_WorkflowTriggerContract(t *testing.T) {
	schema := compileGosutoV1Schema(t)

	tests := []struct {
		name     string
		yamlDoc  string
		wantErr  bool
		errMatch string
	}{
		{
			name: "unknown trigger type rejected",
			yamlDoc: `
apiVersion: gosuto/v1
metadata:
  name: test-agent
trust:
  allowedRooms: ["!room:example.com"]
  allowedSenders: ["@alice:example.com"]
workflow:
  protocols:
    - id: kairo.news.request.v1
      trigger:
        type: matrix.message
        prefix: KAIRO_NEWS_REQUEST
`,
			wantErr:  true,
			errMatch: "enum",
		},
		{
			name: "matrix trigger requires prefix",
			yamlDoc: `
apiVersion: gosuto/v1
metadata:
  name: test-agent
trust:
  allowedRooms: ["!room:example.com"]
  allowedSenders: ["@alice:example.com"]
workflow:
  protocols:
    - id: kairo.news.request.v1
      trigger:
        type: matrix.protocol_message
`,
			wantErr:  true,
			errMatch: "prefix",
		},
		{
			name: "prefix with whitespace rejected",
			yamlDoc: `
apiVersion: gosuto/v1
metadata:
  name: test-agent
trust:
  allowedRooms: ["!room:example.com"]
  allowedSenders: ["@alice:example.com"]
workflow:
  protocols:
    - id: kairo.news.request.v1
      trigger:
        type: matrix.protocol_message
        prefix: KAIRO NEWS REQUEST
`,
			wantErr:  true,
			errMatch: "pattern",
		},
		{
			name: "gateway trigger allows missing prefix",
			yamlDoc: `
apiVersion: gosuto/v1
metadata:
  name: test-agent
trust:
  allowedRooms: ["!room:example.com"]
  allowedSenders: ["@alice:example.com"]
workflow:
  protocols:
    - id: gateway.refresh.v1
      trigger:
        type: gateway.event
`,
			wantErr: false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			var doc map[string]interface{}
			if err := yaml.Unmarshal([]byte(tc.yamlDoc), &doc); err != nil {
				t.Fatalf("yaml.Unmarshal: %v", err)
			}

			err := schema.Validate(doc)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected schema validation error, got nil")
				}
				if tc.errMatch != "" && !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.errMatch)) {
					t.Fatalf("schema error = %v, want substring %q", err, tc.errMatch)
				}
				return
			}

			if err != nil {
				t.Fatalf("schema.Validate: unexpected error: %v", err)
			}
		})
	}
}
