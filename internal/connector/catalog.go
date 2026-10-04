package connector

import (
	"strings"
	"unicode"
)

// ToolLevels is the levels a tool reaches: L0 always; L1 when it is read back (a native L1
// sentence or a ReadBack spec). Postgres adds L2 itself.
func ToolLevels(t ToolSpec) []string {
	if t.SideEffect != SideEffectRead && (t.L1 != "" || t.ReadBack != nil) {
		return []string{"L0", "L1"}
	}
	return []string{"L0"}
}

// ToolTitle turns a tool name into a title: "create_refund" → "Create refund".
func ToolTitle(name string) string {
	s := strings.ReplaceAll(name, "_", " ")
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// CatalogTools is the catalog form of a type's fixed tools, qualified with typ.
func CatalogTools(typ string, specs []ToolSpec) []ToolInfo {
	out := make([]ToolInfo, 0, len(specs))
	for _, t := range specs {
		if t.Internal {
			continue
		}
		rb := ""
		if t.SideEffect != SideEffectRead {
			rb = t.L1
		}
		out = append(out, ToolInfo{Object: "tool", Name: typ + "." + t.Name, Title: ToolTitle(t.Name), Description: t.Description,
			SideEffect: t.SideEffect, Idempotent: t.Idempotent, Levels: ToolLevels(t), ReadBack: rb, RequiresScope: t.RequiresScope,
			InputSchema: t.InputSchema})
	}
	return out
}
