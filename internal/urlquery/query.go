// Package urlquery provides strict helpers for URL query configuration.
package urlquery

import (
	"fmt"
	"net/url"
)

// Validate rejects unknown and repeated query parameters.
func Validate(query url.Values, allowed ...string) error {
	known := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		known[name] = struct{}{}
	}
	for name, values := range query {
		if _, ok := known[name]; !ok {
			return fmt.Errorf("unknown query parameter %q", name)
		}
		if len(values) != 1 {
			return fmt.Errorf("query parameter %q must appear once", name)
		}
	}
	return nil
}

// Bool returns an optional query parameter parsed as a strict boolean.
func Bool(query url.Values, name string) (bool, error) {
	value, ok := query[name]
	if !ok {
		return false, nil
	}
	if len(value) != 1 {
		return false, fmt.Errorf("query parameter %q must appear once", name)
	}
	switch value[0] {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("%s must be true or false", name)
	}
}
