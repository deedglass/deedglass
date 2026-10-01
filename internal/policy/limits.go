package policy

// Limits of policy/v1 (docs/POLICY_LANGUAGE.md §8). Changing one needs an ADR.
const (
	MaxSourceBytes     = 256 << 10 // file_too_large
	MaxYAMLNodes       = 50_000    // yaml_too_complex
	MaxYAMLDepth       = 32        // yaml_too_complex
	MaxRules           = 500       // too_many_rules
	MaxBudgets         = 50        // too_many_budgets
	MaxListValues      = 32        // too_many_values: one match list, labels, approvers, notify
	MaxDescription     = 1_000     // value_too_long (characters)
	MaxReason          = 280       // reason_too_long (warning)
	MaxGlob            = 200       // invalid_glob (S13's tool-name limit)
	MaxExprChars       = 4_096     // cel_too_long
	MaxParserRecursion = 64        // cel_syntax
	MaxCost            = 100_000   // cel_cost (estimated) and the runtime limit (S11)
	MaxNesting         = 2         // cel_nesting
	MaxRegex           = 256       // cel_regex
	MaxDiagnostics     = 50        // then one "and N more" entry
	MaxCalls           = 1_000_000_000
	MaxRequired        = 5
	InterruptEvery     = 64            // cel.InterruptCheckFrequency
	MinExpiry          = 60            // seconds (1m)
	MaxExpiry          = 7 * 24 * 3600 // seconds (7d)
	maxSnippet         = 120           // runes of the user's own text quoted in a message
)

// Size hints for cost estimation (§8).
const (
	hintString     = 262_144
	hintArgs       = 1_024
	hintDyn        = 1_024
	hintList       = 1_024
	hintTables     = 64
	hintFunctions  = 128
	hintLabels     = 32
	hintLabelValue = 128 // S08: label values are at most 128 characters
	hintFactString = 256 // declared fact strings: names, ids, kinds, table and function names
	hintBudgets    = 50
)

// CEL extension versions pinned by policy/v1 (§7.2). They are part of every normalized policy.
const (
	extStringsVersion = 5
	extSetsVersion    = 0
	extListsVersion   = 4
)

// Lang names the language in the normalized document.
const Lang = "deedglass.policy/v1"

// Marker is the value of the `deedglass:` key.
const Marker = "policy/v1"

// DocBase is the doc_url prefix for diagnostics.
const DocBase = "https://docs.deedglass.com/policies/errors/"
