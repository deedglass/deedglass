package httpx

import (
	"bufio"
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if string(want) != string(got) {
		t.Fatalf("%s differs (run with -update after reviewing):\n--- want\n%s\n--- got\n%s", name, want, got)
	}
}

// bannedWords reads scripts/banned_words.txt (whole words, case-insensitive).
func bannedWords(t *testing.T) []*regexp.Regexp {
	t.Helper()
	f, err := os.Open("../../scripts/banned_words.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []*regexp.Regexp
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		l = strings.TrimPrefix(l, "@until-public ")
		w := regexp.QuoteMeta(l)
		w = strings.ReplaceAll(w, `\ `, `[ -]`)
		out = append(out, regexp.MustCompile(`(?i)(^|[^A-Za-z0-9_])`+w+`($|[^A-Za-z0-9_])`))
	}
	return out
}

func TestCatalog(t *testing.T) {
	cat := Catalog()
	if len(cat) != 41 {
		t.Fatalf("the catalog has %d codes, want 41 (24 contract + 9 S03 + 6 S07 + 1 S11 + 1 S12)", len(cat))
	}
	added := map[string]int{}
	codeRE := regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	types := map[ErrType]bool{TypeInvalidRequest: true, TypeAuthentication: true, TypePermission: true, TypePolicy: true, TypeRateLimit: true, TypeAPI: true}
	banned := bannedWords(t)
	for _, c := range cat {
		added[c.Added]++
		if !codeRE.MatchString(string(c.Code)) {
			t.Errorf("%s: not snake_case", c.Code)
		}
		if !types[c.Type] {
			t.Errorf("%s: type %q", c.Code, c.Type)
		}
		if http.StatusText(c.Status) == "" || c.Status < 400 {
			t.Errorf("%s: status %d", c.Code, c.Status)
		}
		for _, m := range []string{c.Message, c.Template} {
			if m == "" {
				continue
			}
			end := strings.HasSuffix(m, ".") || (m == c.Template && strings.HasSuffix(m, "?"))
			if !end || strings.Contains(m, "!") || len(m) > 200 {
				t.Errorf("%s: message must end with '.' (a template may end with '?'), have no '!', and be ≤ 200 characters: %q (%d)", c.Code, m, len(m))
			}
			for _, re := range banned {
				if re.MatchString(m) {
					t.Errorf("%s: banned word %s in %q", c.Code, re, m)
				}
			}
			for _, w := range []string{"Oops", "Unauthorized", "Sorry", "please"} {
				if strings.Contains(m, w) {
					t.Errorf("%s: %q is not the brand voice", c.Code, w)
				}
			}
		}
		if strings.ContainsAny(c.Message, "{}") {
			t.Errorf("%s: the rendered message has a placeholder: %q", c.Code, c.Message)
		}
		if c.Message[0] < 'A' || c.Message[0] > 'Z' {
			t.Errorf("%s: message must start with a capital letter", c.Code)
		}
		if e := E(c.Code); e.Status != c.Status || e.Type != c.Type || e.Message != c.Message {
			t.Errorf("%s: E() disagrees with the catalog", c.Code)
		}
	}
	if added["contract"] != 24 || added["S03"] != 9 || added["S07"] != 6 || added["S11"] != 1 {
		t.Fatalf("added = %v, want 24 contract + 9 S03 + 6 S07 + 1 S11", added)
	}
	// The contract's status decisions (§8).
	for code, want := range map[Code]int{
		CodeIntentRequired: 403, CodeIntentInvalid: 403, CodeIntentReplay: 403, CodeBudgetExceeded: 429,
		CodeInvalidAPIKey: 401, CodeAPIKeyRevoked: 401, CodeAPIKeyExpired: 401, CodeIdempotencyConflict: 422, CodeIdempotencyKeyInUse: 409,
	} {
		if byCode[code].Status != want {
			t.Errorf("%s: status %d, contract says %d", code, byCode[code].Status, want)
		}
	}
	if e := E(Code("no_such_code")); e.Code != CodeInternalError || e.Cause() == nil {
		t.Fatal("an unknown code must render internal_error and keep a cause")
	}
}

func TestCatalogDocs(t *testing.T) {
	b, err := os.ReadFile("../../docs/api/errors.md")
	if err != nil {
		t.Fatalf("docs/api/errors.md: %v", err)
	}
	var headings []string
	for _, l := range strings.Split(string(b), "\n") {
		if h, ok := strings.CutPrefix(l, "## "); ok {
			headings = append(headings, strings.TrimSpace(h))
		}
	}
	var codes []string
	for _, c := range Catalog() {
		codes = append(codes, string(c.Code))
	}
	var got []string
	for _, h := range headings {
		if h != "What this doesn't do" {
			got = append(got, h)
		}
	}
	if strings.Join(got, ",") != strings.Join(codes, ",") {
		t.Fatalf("docs/api/errors.md ## headings must be the catalog codes in order.\n got: %v\nwant: %v", got, codes)
	}
}

func TestEnvelopeGolden(t *testing.T) {
	e := errEndpointNotFound("GET", "/v1/helth", "/v1/health")
	golden(t, "envelope_not_found.golden", errorBody(e, "req_01j9zr3m5k8q2w7x4t6v0b1c9d"))
	golden(t, "envelope_validation.golden", errorBody(ErrValidation("limit", "`limit` must be between 1 and 100."), "req_01j9zr3m5k8q2w7x4t6v0b1c9d"))
}

func TestMessagesFilledAtCallSites(t *testing.T) {
	cases := map[string]*Error{
		"The request body is larger than 1 MiB. Send a smaller body.":                                                                                                      errTooLarge(1 << 20),
		"The request body is larger than 64 KiB. Send a smaller body.":                                                                                                     errTooLarge(64 << 10),
		"The request body is larger than 1000 bytes. Send a smaller body.":                                                                                                 errTooLarge(1000),
		"Too many requests. Retry after 3 s.":                                                                                                                              ErrRateLimited(3),
		"POST isn't supported on /x. Use GET, HEAD or PUT.":                                                                                                                errMethodNotAllowed("POST", "/x", []string{"GET", "HEAD", "PUT"}),
		"There's no endpoint at GET /a?b. Check the path in the API reference.":                                                                                            errEndpointNotFound("GET", "/a\x00b", ""),
		"This key doesn't have the deeds:read scope. Create a key with deeds:read in Console › Agents.":                                                                    ErrInsufficientScope("deeds:read"),
		`Agent "support-bot" is stopped. Deedglass refuses its calls until an admin starts it in Console › Agents.`:                                                        ErrAgentStopped("support-bot"),
		`Agent "support-bot" is suspended. An admin can resume it in Console › Agents.`:                                                                                    ErrAgentSuspended("support-bot"),
		"All agents in Acme are stopped. An owner can start them again in Console › Settings.":                                                                             ErrOrgStopped("Acme"),
		"All agents in Acme are stopped, so admin keys can only read until an owner starts them again in Console › Settings. To change agents now, use a Console session.": ErrOrgStoppedAdminWrite("Acme"),
		"The API key expired at 2026-10-04 10:03 UTC after it was rolled. Use the new key, or create one in Console › Agents.": ErrAPIKeyExpired(
			time.Date(2026, 10, 4, 10, 3, 40, 0, time.UTC)),
		"Too many requests with invalid API keys from this address. Try again in 4 seconds.": ErrKeyRateLimited(4),
	}
	for want, e := range cases {
		if e.Message != want {
			t.Errorf("got %q, want %q", e.Message, want)
		}
	}
	if got := errTimeout(30).Message; !strings.HasPrefix(got, "The request timed out after 30 s.") {
		t.Errorf("timeout: %q", got)
	}
	if got := errEndpointNotFound("GET", "/"+strings.Repeat("a", 500), "").Message; len(got) > 200 {
		t.Errorf("a long path must be clipped: %d characters", len(got))
	}
}
