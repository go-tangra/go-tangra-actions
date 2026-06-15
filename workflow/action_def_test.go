package workflow

import (
	"strings"
	"testing"
)

func TestParseAction_Valid(t *testing.T) {
	src := `
name: install-and-configure
description: install a package and drop a config
inputs:
  pkg:
    required: true
  config_path:
    default: /etc/foo.conf
outputs:
  installed:
    value: "${{ steps.p.outputs.packages }}"
runs:
  using: composite
  steps:
    - id: p
      uses: package
      with: { name: "${{ inputs.pkg }}", state: present }
    - uses: file
      with: { path: "${{ inputs.config_path }}", content: hi }
`
	def, err := ParseAction([]byte(src))
	if err != nil {
		t.Fatalf("ParseAction: %v", err)
	}
	if def.Name != "install-and-configure" {
		t.Errorf("name = %q", def.Name)
	}
	if def.Runs.Using != UsingComposite || len(def.Runs.Steps) != 2 {
		t.Errorf("runs = %+v", def.Runs)
	}
	if !def.Inputs["pkg"].Required {
		t.Error("pkg should be required")
	}
	if def.Outputs["installed"].Value != "${{ steps.p.outputs.packages }}" {
		t.Errorf("output value = %q", def.Outputs["installed"].Value)
	}
}

func TestParseAction_Scripted(t *testing.T) {
	src := `
name: my-js-action
inputs:
  who: { required: true }
outputs:
  result: { value: "${{ steps }}" }
runs:
  using: javascript
  main: dist/index.js
`
	def, err := ParseAction([]byte(src))
	if err != nil {
		t.Fatalf("ParseAction: %v", err)
	}
	if def.Runs.IsComposite() {
		t.Error("should not be composite")
	}
	if def.Runs.Using != "javascript" || def.Runs.Main != "dist/index.js" {
		t.Errorf("runs = %+v", def.Runs)
	}
}

func TestParseAction_Errors(t *testing.T) {
	tests := []struct {
		name    string
		src     string
		wantSub string
	}{
		{
			name:    "missing using",
			src:     "runs:\n  steps: [{run: x}]\n",
			wantSub: "runs.using is required",
		},
		{
			name:    "scripted without main",
			src:     "runs:\n  using: javascript\n",
			wantSub: "runs.main is required",
		},
		{
			name:    "scripted with steps",
			src:     "runs:\n  using: lua\n  main: main.lua\n  steps: [{run: x}]\n",
			wantSub: "runs.steps is only valid for a composite action",
		},
		{
			name:    "composite with main",
			src:     "runs:\n  using: composite\n  main: x.js\n  steps: [{run: x}]\n",
			wantSub: "runs.main is not valid for a composite action",
		},
		{
			name:    "no steps",
			src:     "runs:\n  using: composite\n  steps: []\n",
			wantSub: "at least one step",
		},
		{
			name:    "output without value",
			src:     "outputs:\n  x: { description: y }\nruns:\n  using: composite\n  steps: [{run: echo hi}]\n",
			wantSub: `output "x": missing value`,
		},
		{
			name:    "invalid input name",
			src:     "inputs:\n  \"bad name\": { default: x }\nruns:\n  using: composite\n  steps: [{run: echo hi}]\n",
			wantSub: "invalid name",
		},
		{
			name:    "step both run and uses",
			src:     "runs:\n  using: composite\n  steps:\n    - { run: x, uses: package }\n",
			wantSub: "mutually exclusive",
		},
		{
			name:    "unknown field",
			src:     "runs:\n  using: composite\n  steps: [{run: hi}]\nbogus: 1\n",
			wantSub: "bogus",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseAction([]byte(tt.src))
			if err == nil {
				t.Fatalf("expected error containing %q", tt.wantSub)
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("error = %q, want substring %q", err.Error(), tt.wantSub)
			}
		})
	}
}
