package tool

import (
	"errors"
	"fmt"
)

// Descriptor is a tool's self-description: a summary plus the actions it
// supports, each with its parameters and whether it takes a target. Wired
// into a Backend, it lets a planner build a precise per-action function
// definition instead of making the model guess the tool's shape. It is
// required: NewRegistry panics on a Backend without one.
type Descriptor struct {
	// Summary is a one-line, model-facing description of the tool.
	Summary string

	// Actions are the operations the tool supports; there is at least
	// one, and each Action.Name is a distinct, non-empty verb matching
	// the Call.Action the tool accepts.
	Actions []Action
}

// Action describes one operation a tool supports, matching a Call.Action
// the tool's Invoke accepts.
type Action struct {
	// Name is the verb, matching Call.Action (e.g. "check").
	Name string

	// Description is the model-facing explanation of the action.
	Description string

	// TakesTarget reports whether Call.Target is meaningful for this
	// action. When false, the action ignores Target.
	TakesTarget bool

	// TargetDesc is the model-facing description of the target, used only
	// when TakesTarget is true.
	TargetDesc string

	// Parameters are the typed key/value arguments the action reads from
	// Call.Parameters. It may be empty.
	Parameters []Param
}

// Param describes one typed key/value argument of an action, carried in
// Call.Parameters.
type Param struct {
	// Name is the key in Call.Parameters.
	Name string

	// Type is the JSON-schema scalar type: "string", "number",
	// "integer", or "boolean".
	Type string

	// Required reports whether the action requires the parameter.
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

// Validate enforces the descriptor contract: at least one action, actions
// with distinct non-empty names, and parameters with distinct non-empty
// names and a supported scalar type. Callers use it to reject an invalid
// schema up front rather than at the model API.
func (d Descriptor) Validate() error {
	if len(d.Actions) == 0 {
		return errors.New("descriptor declares no actions")
	}
	seenActions := make(map[string]bool, len(d.Actions))
	for _, a := range d.Actions {
		if a.Name == "" {
			return errors.New("descriptor declares an action with no name")
		}
		if seenActions[a.Name] {
			return fmt.Errorf("descriptor declares duplicate action %q", a.Name)
		}
		seenActions[a.Name] = true
		seenParams := make(map[string]bool, len(a.Parameters))
		for _, p := range a.Parameters {
			if p.Name == "" {
				return fmt.Errorf("action %q declares a parameter with no name", a.Name)
			}
			if seenParams[p.Name] {
				return fmt.Errorf("action %q declares duplicate parameter %q", a.Name, p.Name)
			}
			seenParams[p.Name] = true
			if !validParamTypes[p.Type] {
				return fmt.Errorf("action %q parameter %q declares unsupported type %q", a.Name, p.Name, p.Type)
			}
		}
	}
	return nil
}

// Clone returns a deep copy of d: the Actions and Parameters slices are
// duplicated so the two can be mutated independently. Param holds only
// scalars, so a plain slice copy fully isolates it.
func (d Descriptor) Clone() Descriptor {
	if d.Actions == nil {
		return d
	}
	actions := make([]Action, len(d.Actions))
	for i, a := range d.Actions {
		if a.Parameters != nil {
			params := make([]Param, len(a.Parameters))
			copy(params, a.Parameters)
			a.Parameters = params
		}
		actions[i] = a
	}
	d.Actions = actions
	return d
}
