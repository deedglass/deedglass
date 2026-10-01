package policy

// Codes is every diagnostic code of policy/v1 with its severity, in docs order. Codes are API:
// add, never rename. docs/POLICY_ERRORS.md has one section per code (TestErrorDocs).
var Codes = []struct {
	Code     string
	Severity Severity
}{
	// Reading the file.
	{"file_too_large", SevError},
	{"yaml_syntax", SevError},
	{"yaml_tab_indent", SevError},
	{"yaml_alias", SevError},
	{"yaml_merge_key", SevError},
	{"yaml_tag", SevError},
	{"yaml_multi_document", SevError},
	{"yaml_duplicate_key", SevError},
	{"yaml_too_complex", SevError},
	// Structure.
	{"missing_marker", SevError},
	{"unsupported_format", SevError},
	{"unknown_key", SevError},
	{"missing_key", SevError},
	{"wrong_type", SevError},
	{"conflicting_keys", SevError},
	{"invalid_name", SevError},
	{"invalid_id", SevError},
	{"duplicate_id", SevError},
	{"too_many_rules", SevError},
	{"too_many_budgets", SevError},
	{"too_many_values", SevError},
	{"value_too_long", SevError},
	// Values.
	{"invalid_decision", SevError},
	{"invalid_value", SevError},
	{"out_of_range", SevError},
	{"reason_missing", SevError},
	{"control_character", SevError},
	{"invalid_glob", SevError},
	{"invalid_side_effect", SevError},
	{"invalid_label", SevError},
	{"invalid_duration", SevError},
	{"money_float", SevError},
	{"invalid_money", SevError},
	{"unknown_currency", SevError},
	{"money_precision", SevError},
	{"invalid_approvers", SevError},
	{"approvals_unsatisfiable", SevError},
	// CEL.
	{"cel_syntax", SevError},
	{"cel_undeclared", SevError},
	{"cel_no_field", SevError},
	{"cel_type", SevError},
	{"cel_not_bool", SevError},
	{"cel_not_int", SevError},
	{"cel_cost", SevError},
	{"cel_nesting", SevError},
	{"cel_too_long", SevError},
	{"cel_regex", SevError},
	{"cel_constant_required", SevError},
	// Warnings.
	{"reason_too_long", SevWarning},
	{"unknown_connector_type", SevWarning},
	{"ignored_field", SevWarning},
	{"rule_unreachable", SevWarning},
	{"default_allow", SevWarning},
	{"secret_in_policy", SevWarning},
	{"reason_style", SevWarning},
	// The compiler itself.
	{"more_diagnostics", SevError},
	{"internal_compiler_error", SevError},
}
