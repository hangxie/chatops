package tool

import (
	"errors"
	"fmt"
)

// Descriptor is a tool's self-description: a model-facing summary plus the
// flat parameters the tool reads. Wired into a Backend, it lets a planner
// build a precise, typed function definition instead of making the model
// guess the tool's shape. It maps onto a Model Context Protocol tool: the
// scheme is the name, Description the description, and Parameters the
// properties of a flat input schema. It is required: NewRegistry panics on
// a Backend without one.
type Descriptor struct {
	// Description is the one-line, model-facing explanation of what the
	// tool does. It is required.
	Description string

	// Parameters are the typed key/value arguments the tool reads from
	// Call.Arguments. It may be empty.
	Parameters []Param
}

// Param describes one typed key/value argument of a tool, carried in
// Call.Arguments.
type Param struct {
	// Name is the key in Call.Arguments.
	Name string

	// Type is the JSON-schema scalar type: "string", "number",
	// "integer", or "boolean".
	Type string

	// Required reports whether the tool requires the parameter.
	Required bool

	// Description is the model-facing explanation of the parameter.
	Description string
}

// validParamTypes is the set of JSON-schema scalar types a parameter may
// declare; the empty string is allowed and defaults to "string" when a
// schema is built from the descriptor.
var validParamTypes = map[string]bool{
	"":        true,
	"string":  true,
	"number":  true,
	"integer": true,
	"boolean": true,
}

// Validate enforces the descriptor contract: a non-empty description and
// parameters with distinct non-empty names and a supported scalar type.
// Callers use it to reject an invalid schema up front rather than at the
// model API.
func (d Descriptor) Validate() error {
	if d.Description == "" {
		return errors.New("descriptor declares no description")
	}
	seenParams := make(map[string]bool, len(d.Parameters))
	for _, p := range d.Parameters {
		if p.Name == "" {
			return errors.New("descriptor declares a parameter with no name")
		}
		if seenParams[p.Name] {
			return fmt.Errorf("descriptor declares duplicate parameter %q", p.Name)
		}
		seenParams[p.Name] = true
		if !validParamTypes[p.Type] {
			return fmt.Errorf("descriptor parameter %q declares unsupported type %q", p.Name, p.Type)
		}
	}
	return nil
}

// Clone returns a deep copy of d: the Parameters slice is duplicated so the
// two can be mutated independently. Param holds only scalars, so a plain
// slice copy fully isolates it.
func (d Descriptor) Clone() Descriptor {
	if d.Parameters == nil {
		return d
	}
	params := make([]Param, len(d.Parameters))
	copy(params, d.Parameters)
	d.Parameters = params
	return d
}
