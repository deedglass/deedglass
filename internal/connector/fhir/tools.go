package fhir

import (
	"encoding/json"
	"slices"

	"github.com/deedglass/deedglass/internal/connector"
)

const idPattern = `^[A-Za-z0-9\-.]{1,64}$`

type obj = map[string]any

func str(desc string) obj { return obj{"type": "string", "description": desc} }

func idArg(desc string) obj {
	return obj{"type": "string", "pattern": idPattern, "maxLength": 64, "description": desc}
}

// tool is one interaction's spec, before the resource type enum is filled in.
type tool struct {
	name, desc, l1 string
	side           connector.SideEffect
	props          obj
	required       []string
}

func tools(ifMatch string) []tool {
	ifm := idArg("The version id the agent read (meta.versionId); sent as If-Match: W/\"<id>\". A stale one fails with fhir_version_conflict.")
	write := []string{"resource_type", "id"}
	if ifMatch == "required" {
		write = append(write, "if_match")
	}
	params := obj{"type": "object", "description": "Search parameters: each value a string or an array of strings, like {\"code\": \"http://loinc.org|8867-4\", \"_count\": \"20\"}."}
	return []tool{
		{name: "read", desc: "Read the current version of one resource.", side: connector.SideEffectRead,
			props: obj{"id": idArg("The resource id.")}, required: []string{"resource_type", "id"}},
		{name: "vread", desc: "Read one version of a resource.", side: connector.SideEffectRead,
			props: obj{"id": idArg("The resource id."), "version_id": idArg("The version id.")}, required: []string{"resource_type", "id", "version_id"}},
		{name: "search", desc: "Search resources of one type with FHIR search parameters.", side: connector.SideEffectRead,
			props: obj{"params": params}, required: []string{"resource_type"}},
		{name: "history", desc: "The version history of one resource, or of a type when id is left out.", side: connector.SideEffectRead,
			props:    obj{"id": idArg("The resource id; leave it out for the type's history."), "params": obj{"type": "object", "description": "Only _since, _at and _count."}},
			required: []string{"resource_type"}},
		{name: "create", desc: "Create a resource. if_none_exist makes it a conditional create: the server creates nothing when a match exists.",
			l1:   "The version the server reported, read back and compared field by field with the resource sent; an existing resource returned for a conditional create doesn't count as created.",
			side: connector.SideEffectWrite, props: obj{"resource": obj{"type": "object", "description": "The resource as FHIR JSON; resourceType must equal resource_type."},
				"if_none_exist": obj{"type": "string", "maxLength": 1024, "description": "Search parameters for a conditional create, like identifier=http://example.org/mrn|12345."}},
			required: []string{"resource_type", "resource"}},
		{name: "update", desc: "Replace a resource with a new version (or create it at that id when the server allows it).",
			l1:   "The version the server reported, read back and compared field by field with the resource sent; an update the server kept at the same version is noted.",
			side: connector.SideEffectWrite, props: obj{"id": idArg("The resource id."), "resource": obj{"type": "object", "description": "The whole resource as FHIR JSON; its id, when set, must equal id."}, "if_match": ifm},
			required: append(slices.Clone(write), "resource")},
		{name: "patch", desc: "Change parts of a resource with a JSON Patch (json_patch) or a FHIRPath Patch Parameters resource (fhirpath_patch). Send exactly one.",
			l1:   "The version the server reported, read back; the paths a JSON Patch adds, replaces or removes are checked.",
			side: connector.SideEffectWrite, props: obj{"id": idArg("The resource id."), "if_match": ifm,
				"json_patch":     obj{"type": "array", "items": obj{"type": "object"}, "description": "RFC 6902 operations, like [{\"op\":\"replace\",\"path\":\"/status\",\"value\":\"final\"}]."},
				"fhirpath_patch": obj{"type": "object", "description": "A FHIRPath Patch Parameters resource."}},
			required: write},
		{name: "delete", desc: "Delete a resource. The server keeps its history; a later read answers 410 Gone.",
			l1:   "The resource read again: it must answer 410 Gone (or 404).",
			side: connector.SideEffectDestructive, props: obj{"id": idArg("The resource id."), "if_match": ifm}, required: write},
	}
}

// toolSpecs are the tools for the allowed interactions, with resource_type limited to types (nil:
// any type name, for the catalog).
func toolSpecs(types, inters []string, ifMatch string) []connector.ToolSpec {
	rt := obj{"type": "string", "pattern": `^[A-Z][A-Za-z]{1,63}$`, "description": "The FHIR resource type, one of the connector's allowed_resource_types."}
	if types != nil {
		rt = obj{"type": "string", "enum": types, "description": "The FHIR resource type."}
	}
	var out []connector.ToolSpec
	for _, t := range tools(ifMatch) {
		if !slices.Contains(inters, t.name) {
			continue
		}
		props := obj{"resource_type": rt}
		for k, v := range t.props {
			props[k] = v
		}
		schema, _ := json.Marshal(obj{"type": "object", "properties": props, "required": t.required, "additionalProperties": false})
		out = append(out, connector.ToolSpec{Name: t.name, Description: t.desc, InputSchema: schema, SideEffect: t.side,
			Idempotent: t.side == connector.SideEffectRead, L1: t.l1})
	}
	return out
}
