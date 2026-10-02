package gate

import (
	"strings"
	"testing"
)

func TestEvidenceJSON(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]string
		want string
	}{
		{"none", nil, ""},
		{"postgres", map[string]string{"xid8": "7461", "marker_lsn": "0/3E64FD0", "sql_digest": "sha256:" + strings.Repeat("a", 64), "command_tag": "UPDATE 1"},
			`{"command_tag":"UPDATE 1","marker_lsn":"0/3E64FD0","sql_digest":"sha256:` + strings.Repeat("a", 64) + `","xid8":"7461"}`},
		{"bad key dropped", map[string]string{"Xid": "1", "xid8": "2"}, `{"xid8":"2"}`},
		{"customer value dropped", map[string]string{"email": "a@b.example\n", "xid8": "2"}, `{"xid8":"2"}`},
		{"long value dropped", map[string]string{"note": strings.Repeat("x", 129)}, ""},
		{"quotes dropped", map[string]string{"q": `say "hi"`}, ""},
	}
	for _, c := range cases {
		got := string(EvidenceJSON(c.in))
		if got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
	many := map[string]string{}
	for i := 0; i < 20; i++ {
		many[string(rune('a'+i))+"k"] = strings.Repeat("v", 120)
	}
	b := EvidenceJSON(many)
	if len(b) == 0 || len(b) > maxEvidenceBytes {
		t.Fatalf("%d bytes", len(b))
	}
}
