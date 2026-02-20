// Package templates provides loading and interpolation of Gosuto agent
// configuration templates.
//
// Each template lives in a named subdirectory and must contain a gosuto.yaml
// file using Go text/template syntax for variable substitution.
//
// Typical layout (relative to the templates root):
//
//	cron-agent/gosuto.yaml
//	browser-agent/gosuto.yaml
package templates

import (
	"bytes"
	"fmt"
	"io/fs"
	"text/template"

	gosutospec "github.com/bdobrica/Ruriko/common/spec/gosuto"
	"gopkg.in/yaml.v3"
)

// TemplateVars holds values interpolated into a Gosuto YAML template.
type TemplateVars struct {
	// AgentName is the Ruriko agent ID (e.g. "my-agent").
	AgentName string

	// DisplayName is the human-readable agent display name.
	DisplayName string

	// AdminRoom is the Matrix room ID of the primary admin room.
	AdminRoom string

	// AgentMXID is the Matrix user ID provisioned for the agent. May be empty
	// before a Matrix account has been provisioned.
	AgentMXID string

	// OperatorMXID is the Matrix user ID of the operator who triggered
	// provisioning. Used as a default approver in templates that enable the
	// approval workflow.
	OperatorMXID string
}

// Registry resolves and renders Gosuto templates from a filesystem root.
//
// The root fs.FS is expected to contain sub-directories named after templates;
// each must hold a gosuto.yaml file.
//
// Example:
//
//	reg := templates.NewRegistry(os.DirFS("/etc/ruriko/templates"))
//	yaml, err := reg.Render("cron-agent", vars)
type Registry struct {
	root fs.FS
}

// NewRegistry creates a Registry backed by the provided filesystem root.
func NewRegistry(root fs.FS) *Registry {
	return &Registry{root: root}
}

// List returns the names of all templates that contain a gosuto.yaml file.
func (r *Registry) List() ([]string, error) {
	entries, err := fs.ReadDir(r.root, ".")
	if err != nil {
		return nil, fmt.Errorf("listing templates: %w", err)
	}

	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := fs.Stat(r.root, e.Name()+"/gosuto.yaml"); err == nil {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// Render loads gosuto.yaml for the named template, interpolates vars, and
// returns the rendered YAML as a byte slice ready to be stored as a Gosuto
// version.
//
// Templates are trusted operator content loaded from disk. User-submitted
// template content must NOT be used here — text/template allows arbitrary
// pipeline chaining that could be exploited.
func (r *Registry) Render(name string, vars TemplateVars) ([]byte, error) {
	path := name + "/gosuto.yaml"

	raw, err := fs.ReadFile(r.root, path)
	if err != nil {
		return nil, fmt.Errorf("template %q: %w", name, err)
	}

	// Option "missingkey=error" causes the template to fail loudly if a
	// TemplateVars field referenced in the template does not exist, instead
	// of silently inserting "<no value>".
	tmpl, err := template.New(path).Option("missingkey=error").Parse(string(raw))
	if err != nil {
		return nil, fmt.Errorf("template %q: parse: %w", name, err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vars); err != nil {
		return nil, fmt.Errorf("template %q: render: %w", name, err)
	}

	return buf.Bytes(), nil
}

// RequiredSecrets renders the named template for agentName and returns the
// names of all SecretRef entries that are marked required=true in the
// resulting gosuto.yaml.
//
// This is used by the natural-language provisioning wizard to determine which
// secrets must be stored in Ruriko before the agent can be provisioned.
func (r *Registry) RequiredSecrets(templateName, agentName string) ([]string, error) {
	vars := TemplateVars{
		AgentName:   agentName,
		DisplayName: agentName,
	}
	rendered, err := r.Render(templateName, vars)
	if err != nil {
		return nil, fmt.Errorf("RequiredSecrets: render template: %w", err)
	}

	var cfg gosutospec.Config
	if err := yaml.Unmarshal(rendered, &cfg); err != nil {
		return nil, fmt.Errorf("RequiredSecrets: parse gosuto yaml: %w", err)
	}

	names := make([]string, 0, len(cfg.Secrets))
	for _, ref := range cfg.Secrets {
		if ref.Required {
			names = append(names, ref.Name)
		}
	}
	return names, nil
}
