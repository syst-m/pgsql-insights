package discovery

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// The CNPG CRDs have no generated Go types in this repo's dependency set
// (R13.10 forbids `replace`, and importing cnpg's api package would drag in
// their whole module graph), so Cluster and Pooler are read as
// unstructured.Unstructured.Object maps. These accessors are the whole of that
// reading: every field R1.5 lists is fetched through one of them, which keeps
// the "what do we read off the CR" question answerable with a single grep and
// makes each field's absent-vs-malformed behaviour a testable unit.
//
// They take map[string]any rather than *unstructured.Unstructured so the same
// readers work on a nested sub-object (a status.conditions entry) without
// wrapping it back into an Unstructured first.

// nested walks to a path in a nested map and returns the raw value.
func nested(obj map[string]any, fields ...string) (any, bool) {
	var cur any = obj
	for _, f := range fields {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[f]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// mapString reads a string out of a plain map (a condition entry, say),
// returning "" for a missing key or a value that is not a string.
func mapString(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// nestedStringE is nestedString with the two failure modes distinguished: a
// missing path and a path holding the wrong type. R1.5's fields are all strings
// or ints, so a wrong type means the CR is malformed rather than absent, and a
// caller that cares can report it instead of rendering a blank.
func nestedStringE(obj map[string]any, fields ...string) (string, error) {
	v, ok := nested(obj, fields...)
	if !ok {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s: expected string, got %T", pathString(fields...), v)
	}
	return s, nil
}

// nestedString reads a string at a nested path, returning "" when the path is
// absent or holds a non-string. Use nestedStringE when the difference matters.
func nestedString(obj map[string]any, fields ...string) string {
	v, _ := nested(obj, fields...)
	s, _ := v.(string)
	return s
}

// nestedIntE reads a number at a nested path. The JSON decoder yields int64,
// float64 or json.Number; a string is also accepted because a hand-applied
// manifest with `instances: "3"` is a real shape in the wild, and reporting 0
// for it would be a silent lie.
func nestedIntE(obj map[string]any, fields ...string) (int, error) {
	v, ok := nested(obj, fields...)
	if !ok {
		return 0, nil
	}
	switch n := v.(type) {
	case int64:
		return int(n), nil
	case int:
		return n, nil
	case float64:
		return int(n), nil
	case json.Number:
		i, err := n.Int64()
		return int(i), err
	case string:
		return strconv.Atoi(n)
	default:
		return 0, fmt.Errorf("%s: expected number, got %T", pathString(fields...), v)
	}
}

// nestedInt is nestedIntE with the error dropped.
func nestedInt(obj map[string]any, fields ...string) int {
	i, _ := nestedIntE(obj, fields...)
	return i
}

// nestedStringMapE reads a string map at a nested path (R1.5's certificate
// expirations, the pooler's pgbouncer parameters).
func nestedStringMapE(obj map[string]any, fields ...string) (map[string]string, error) {
	v, ok := nested(obj, fields...)
	if !ok {
		return nil, nil
	}
	m, err := toStringMap(v)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", pathString(fields...), err)
	}
	return m, nil
}

// There is deliberately no error-dropping nestedStringMap sibling: every call
// site here (build.go's certificate expirations, poolers.go's pgbouncer
// parameters) wants the error, because a malformed map is a fact about the
// cluster that R1.5's view has to report rather than render as "no entries".

// nestedSlice reads a slice at a nested path (status.conditions).
func nestedSlice(obj map[string]any, fields ...string) ([]any, bool) {
	v, ok := nested(obj, fields...)
	if !ok {
		return nil, false
	}
	s, ok := v.([]any)
	return s, ok
}

// toStringMap coerces a decoded JSON object to map[string]string. The decoder
// gives map[string]any; a typed map[string]string is what the unstructured
// converter produces for a typed field, so both are accepted.
func toStringMap(v any) (map[string]string, error) {
	switch m := v.(type) {
	case map[string]string:
		return m, nil
	case map[string]any:
		out := make(map[string]string, len(m))
		for k, val := range m {
			s, ok := val.(string)
			if !ok {
				return nil, fmt.Errorf("key %q: expected string, got %T", k, val)
			}
			out[k] = s
		}
		return out, nil
	default:
		return nil, fmt.Errorf("expected object, got %T", v)
	}
}

// pathString renders a field path for error messages:
// ("status","certificates","expirations") -> "status.certificates.expirations".
func pathString(fields ...string) string {
	out := ""
	for i, f := range fields {
		if i > 0 {
			out += "."
		}
		out += f
	}
	return out
}
