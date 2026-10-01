package sqlguard

import (
	"math/rand/v2"
	"strings"
	"testing"
	"time"
)

func TestAnalyzeOptions(t *testing.T) {
	f := Analyze("select * from orders", Options{DefaultSchema: "app"})
	if len(f.Tables) != 1 || f.Tables[0] != "app.orders" {
		t.Fatalf("default schema: %v", f.Tables)
	}
	f = Analyze("SELECT 1", Options{MaxBytes: 4})
	if !f.Ambiguous || f.Reasons[0] != ReasonTooLarge || f.Category != CatUnknown || !f.Writes {
		t.Fatalf("too_large: %+v", f)
	}
	f = Analyze("SELECT ((1))", Options{MaxDepth: 1})
	if !f.Ambiguous || f.Reasons[0] != ReasonTooDeep {
		t.Fatalf("too_deep: %+v", f)
	}
	f = Analyze("SELECT 1; SELECT 2; SELECT 3", Options{MaxStatements: 2})
	if !f.Ambiguous || f.Statements != 2 || f.Reasons[0] != ReasonTooManyStatements {
		t.Fatalf("too_many_statements: %+v", f)
	}
	if Severity("nonsense") != Severity(CatUnknown) || Severity(CatNone) != 0 {
		t.Fatal("Severity")
	}
	for _, c := range []string{CatNone, CatRead, CatMaintenance, CatSession, CatTransaction, CatWrite, CatDDL, CatPrivilege, CatServerCode} {
		if Severity(c) >= Severity(CatUnknown) {
			t.Fatalf("%s must rank below unknown", c)
		}
	}
	if s := (Statement{}); !s.Empty() {
		t.Fatal("Statement.Empty")
	}
}

func TestFunctionLists(t *testing.T) {
	cases := map[string]fnClass{
		"pg_read_file": fnDangerous, "pg_catalog.pg_read_file": fnDangerous, "dblink_connect": fnDangerous,
		"query_to_xml_and_xmlschema": fnDangerous, "pg_sleep_for": fnDangerous, "pg_advisory_xact_lock_shared": fnDangerous,
		"pg_create_logical_replication_slot": fnDangerous, "pg_replication_origin_create": fnDangerous,
		"pg_advisory_whatever": fnUnknown, "nextval": fnWrite, "count": fnSafe, "lower": fnSafe,
		"merge_action": fnSafe, "app.cleanup_everything": fnUnknown, "pg_notify": fnWrite,
	}
	for name, want := range cases {
		if got := classifyFunction(name); got != want {
			t.Errorf("classifyFunction(%q) = %d, want %d", name, got, want)
		}
	}
	for _, r := range Dangerous {
		if r.Reason == "" {
			t.Errorf("%+v has no reason", r)
		}
	}
	if DangerousReason("pg_sleep") == "" || DangerousReason("x.pg_read_file") == "" || DangerousReason("count") != "" {
		t.Fatal("DangerousReason")
	}
	if n := SafeFunctionCount(); n < 150 {
		t.Fatalf("the safe list has %d entries; S11 asks for about 150", n)
	}
}

// TestInvariance: comments between tokens, keyword case and extra whitespace never change kind,
// category or multi (S11 §6).
func TestInvariance(t *testing.T) {
	c := loadCorpus(t)
	rng := rand.New(rand.NewPCG(1, 2))
	for _, k := range c.Cases {
		if k.SQL == nil {
			continue
		}
		base := Analyze(*k.SQL, Options{})
		toks, err := Tokenize(*k.SQL)
		if err != nil {
			continue
		}
		for variant := 0; variant < 3; variant++ {
			var b strings.Builder
			for i, tk := range toks {
				switch {
				case tk.Kind == Ident && variant == 0:
					b.WriteString(randomCase(rng, tk.Text))
				default:
					b.WriteString(tk.Text)
				}
				if i < len(toks)-1 && tk.Kind != LineComment {
					switch variant {
					case 1:
						if safeGap(tk, toks[i+1]) {
							b.WriteString("/**/")
						}
					case 2:
						if safeGap(tk, toks[i+1]) {
							b.WriteString(" \n\t")
						}
					}
				}
			}
			got := Analyze(b.String(), Options{})
			if got.Kind != base.Kind || got.Category != base.Category || got.Multi != base.Multi {
				t.Errorf("%s variant %d %q: kind %s/%s category %s/%s multi %v/%v", k.ID, variant, b.String(),
					base.Kind, got.Kind, base.Category, got.Category, base.Multi, got.Multi)
			}
		}
	}
}

// safeGap reports whether a comment or whitespace may go between a and b without changing the
// tokens (not inside an operator pair, a prefix string, a number or a dollar-quote tag).
func safeGap(a, b Token) bool {
	if a.Kind == Op && b.Kind == Op {
		return false
	}
	if a.Kind == Op || b.Kind == Op {
		return false // "a.b" style operators and casts keep their meaning, but "<" "=" would not
	}
	if a.Kind == Punct && a.Text == "." || b.Kind == Punct && b.Text == "." {
		return true
	}
	return true
}

func randomCase(rng *rand.Rand, s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' && rng.IntN(2) == 0 {
			b[i] = c - 32
		} else if c >= 'A' && c <= 'Z' && rng.IntN(2) == 0 {
			b[i] = c + 32
		}
	}
	return string(b)
}

// TestTruncationFailsClosed: cutting a write, DDL, privilege or server-code statement at any byte
// never yields a read with ambiguous false (S11 §6, spot-checked on the corpus).
func TestTruncationFailsClosed(t *testing.T) {
	c := loadCorpus(t)
	for _, k := range c.Cases {
		if k.SQL == nil {
			continue
		}
		full := Analyze(*k.SQL, Options{})
		if full.Ambiguous || Severity(full.Category) <= Severity(CatRead) {
			continue
		}
		s := *k.SQL
		for n := 1; n < len(s); n++ {
			f := Analyze(s[:n], Options{})
			if f.Ambiguous || f.Statements == 0 {
				continue
			}
			// A prefix may legitimately be a different, complete statement ("DELETE FROM orders" of
			// "… WHERE id = 1"); what must never happen is a write prefix reading as a plain read.
			first := strings.ToLower(strings.TrimSpace(s[:n]))
			if f.Category == CatRead && (strings.HasPrefix(first, "delete") || strings.HasPrefix(first, "update") ||
				strings.HasPrefix(first, "insert") || strings.HasPrefix(first, "drop") || strings.HasPrefix(first, "truncate")) {
				t.Errorf("%s: prefix %q of a %s statement reads as %s", k.ID, s[:n], full.Category, f.Category)
			}
		}
	}
}

// TestNeverReadWhenDangerous is the §7 "must never happen" row: DDL, server code or several
// statements are never read with ambiguous false.
func TestNeverReadWhenDangerous(t *testing.T) {
	for _, s := range []string{
		"DROP TABLE t", "SELECT 1; SELECT 2", "DO $$ $$", "COPY t TO PROGRAM 'x'", "WITH d AS (DELETE FROM t) SELECT 1",
		"EXPLAIN ANALYZE DELETE FROM t", "SELECT * INTO t2 FROM t", "CREATE TABLE x (id int)", "GRANT ALL ON t TO PUBLIC",
	} {
		f := Analyze(s, Options{})
		if f.Category == CatRead && !f.Ambiguous {
			t.Errorf("%q reads as read", s)
		}
	}
}

func FuzzAnalyze(f *testing.F) {
	for _, s := range corpusSeeds(f) {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		start := time.Now()
		got := Analyze(s, Options{})
		if d := time.Since(start); d > 50*time.Millisecond && len(s) <= DefaultMaxBytes {
			// The fuzzer runs one worker per CPU, so a slow run may be scheduling: time the best of 3.
			best := d
			for range 3 {
				st := time.Now()
				Analyze(s, Options{})
				best = min(best, time.Since(st))
			}
			if best > 50*time.Millisecond {
				t.Fatalf("Analyze took %v on %d bytes", best, len(s))
			}
		}
		if got.Statements > DefaultMaxStatements || !got.Present || got.Bytes != len(s) {
			t.Fatalf("facts: %+v", got)
		}
		if got.Ambiguous != (len(got.Reasons) > 0) || (got.Ambiguous && got.Category != CatUnknown) {
			t.Fatalf("ambiguity: %+v", got)
		}
		if got.Statements > 1 && !got.Multi || got.Statements != len(got.Kinds) && got.Statements != DefaultMaxStatements {
			t.Fatalf("counts: %+v", got)
		}
		for _, l := range [][]string{got.Kinds, got.WriteKinds, got.Tables, got.Functions, got.DangerousFunctions,
			got.WriteFunctions, got.UnknownFunctions, got.Reasons} {
			if l == nil {
				t.Fatalf("a nil list: %+v", got)
			}
		}
	})
}
