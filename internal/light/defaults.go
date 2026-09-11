package light

// DisplayOnlySegments encodes explicit off, not "preserve current lighting".
func DisplayOnlySegments() []map[string]any {
	return []map[string]any{{"mode": "steady", "duration_s": float64(8), "brightness": float64(0), "color": map[string]any{"r": float64(0), "g": float64(0), "b": float64(0)}}}
}

// NormalizeOneShotSegments fills absent fields without mutating callers or saved rules.
// Explicit null, zero and invalid values remain subject to strict validation.
func NormalizeOneShotSegments(raw any) any {
	arr, ok := raw.([]any)
	if !ok {
		return raw
	}
	out := make([]any, len(arr))
	for i, value := range arr {
		segment, ok := value.(map[string]any)
		if !ok {
			out[i] = value
			continue
		}
		copy := map[string]any{"brightness": float64(128), "color": map[string]any{"r": float64(255), "g": float64(255), "b": float64(255)}}
		for k, v := range segment {
			copy[k] = v
		}
		out[i] = copy
	}
	return out
}
