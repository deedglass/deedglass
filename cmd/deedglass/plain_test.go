package main

import "testing"

func TestPlainError(t *testing.T) {
	for in, want := range map[string]string{
		"can't open --events: open /x/y.gz: no such file or directory":                                            "can't open --events: /x/y.gz doesn't exist",
		"can't read a.yaml: no such file or directory":                                                            "can't read a.yaml: no such file",
		"dial tcp 127.0.0.1:5432: connect: connection refused":                                                    "nothing is listening at 127.0.0.1:5432",
		"Get \"https://x\": context deadline exceeded":                                                            "Get \"https://x\": it took too long",
		"invalid character 'n' looking for beginning of value":                                                    "isn't valid JSON",
		`strconv.ParseInt: parsing "abc": invalid syntax`:                                                         `"abc" isn't a number`,
		"the DSN can't be read: cannot parse `notaurl`: failed to parse as keyword/value (invalid keyword/value)": "the DSN can't be read: `notaurl` isn't a Postgres URL",
		"Blocked by policy \"x\".":                                                                                "Blocked by policy \"x\".",
	} {
		if got := plainError(in); got != want {
			t.Errorf("plainError(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCapitalize(t *testing.T) {
	for in, want := range map[string]string{
		"there's no local stack": "There's no local stack",
		"deedglass init failed":  "deedglass init failed",
		"--events is required":   "--events is required",
		"act_01 wasn't found":    "act_01 wasn't found",
		"a.yaml can't be read":   "a.yaml can't be read",
		"":                       "",
	} {
		if got := capitalize(in); got != want {
			t.Errorf("capitalize(%q) = %q, want %q", in, got, want)
		}
	}
}
