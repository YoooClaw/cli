package healthdata

import (
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"reflect"
)

//go:embed semantics.json
var semanticsJSON []byte

// FieldSemantics is interpretation metadata, separate from evidence and its hashes.
type FieldSemantics struct {
	FormatVersion     int               `json:"formatVersion"`
	Source            string            `json:"source"`
	DataSchemaVersion int               `json:"dataSchemaVersion"`
	Revision          string            `json:"revision"`
	Sections          []SemanticSection `json:"sections"`
}
type SemanticSection struct {
	Type      string   `json:"type"`
	MatchKeys []string `json:"matchKeys"`
	Rules     []string `json:"rules"`
}

func semanticsCatalog() FieldSemantics {
	var result FieldSemantics
	// A malformed embedded resource is a build defect, never a user-data error.
	if err := json.Unmarshal(semanticsJSON, &result); err != nil {
		panic(err)
	}
	result.Revision = fmt.Sprintf("sha256:%x", sha256.Sum256(semanticsJSON))
	return result
}

// Semantics returns all sections by default; explicit types always include common.
// matchKeys lets file-reading clients select sections without their own type list.
func Semantics(types ...string) (FieldSemantics, error) {
	catalog := semanticsCatalog()
	if len(types) == 0 {
		return catalog, nil
	}
	selected := map[string]bool{"common": true}
	for _, kind := range types {
		found := false
		for _, section := range catalog.Sections {
			if section.Type == kind {
				found = true
				break
			}
		}
		if !found {
			return FieldSemantics{}, fmt.Errorf("unknown health semantics type %q; use health-data schema to list types", kind)
		}
		selected[kind] = true
	}
	return selectSemantics(catalog, selected), nil
}
func selectSemantics(catalog FieldSemantics, selected map[string]bool) FieldSemantics {
	sections := []SemanticSection{}
	for _, s := range catalog.Sections {
		if selected[s.Type] {
			sections = append(sections, s)
		}
	}
	catalog.Sections = sections
	return catalog
}

// SemanticsFor selects from the actual returned page or batch (current snapshots only).
// Selection is declared in the embedded catalog, not duplicated in callers.
func SemanticsFor(data ...map[string]any) FieldSemantics {
	catalog := semanticsCatalog()
	selected := map[string]bool{"common": true}
	for _, section := range catalog.Sections {
		for _, row := range data {
			for _, key := range section.MatchKeys {
				if meaningfulSemanticValue(row[key]) {
					selected[section.Type] = true
				}
			}
		}
	}
	return selectSemantics(catalog, selected)
}
func meaningfulSemanticValue(value any) bool {
	if value == nil {
		return false
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map, reflect.String:
		return v.Len() > 0
	default:
		return true
	}
}
