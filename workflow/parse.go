package workflow

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// Parse decodes a workflow from YAML and validates it. Unknown fields are
// rejected (strict decoding) so typos in keys fail loudly at the boundary
// rather than being silently ignored. A parsed workflow is guaranteed to have
// passed Validate.
func Parse(data []byte) (*Workflow, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)

	var wf Workflow
	if err := dec.Decode(&wf); err != nil {
		return nil, fmt.Errorf("parse workflow: %w", err)
	}

	if err := wf.Validate(); err != nil {
		return nil, err
	}
	return &wf, nil
}
