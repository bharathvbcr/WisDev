package wisdev

func MinInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func MaxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// IntValue extracts an int from an any value returned from a map[string]any
// (where JSON numbers decode as float64). Returns 0 for unrecognised types.
func IntValue(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case float64:
		return int(t)
	case int64:
		return int(t)
	case int32:
		return int(t)
	}
	return 0
}

// IntValue64 extracts an int64 from an any value returned from a map[string]any.
// Returns 0 for unrecognised types.
func IntValue64(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case float64:
		return int64(t)
	case int:
		return int64(t)
	case int32:
		return int64(t)
	}
	return 0
}

// AsFloat extracts a float64 from an any value. Returns 0.0 for unrecognised types.
func AsFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case int32:
		return float64(t)
	}
	return 0.0
}
