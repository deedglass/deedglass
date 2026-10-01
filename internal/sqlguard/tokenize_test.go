package sqlguard

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

// kindsOf renders tokens as "Kind:text" separated by spaces, without Space tokens.
func kindsOf(toks []Token) string {
	var b []string
	for _, t := range toks {
		if t.Kind == Space {
			continue
		}
		b = append(b, t.Kind.String()+":"+t.Text)
	}
	return strings.Join(b, " ")
}

func TestTokenizeTable(t *testing.T) {
	cases := []struct {
		in, want, err string
	}{
		{"", "", ""},
		{"   \t\r\n\f\v", "", ""},
		{"SELECT 1", "Ident:SELECT Number:1", ""},
		{"-- c\nx", "LineComment:-- c Ident:x", ""},
		{"--c\r\nx", "LineComment:--c Ident:x", ""},
		{"/* a */x", "BlockComment:/* a */ Ident:x", ""},
		{"/* a /* b */ c */x", "BlockComment:/* a /* b */ c */ Ident:x", ""},
		{"/* a /* b */ c", "BlockComment:/* a /* b */ c", ReasonUnterminatedComment},
		{"'it''s'", "String:'it''s'", ""},
		{"'a\\b'", "String:'a\\b'", ReasonBackslashInString},
		{"E'a\\'b'", "EString:E'a\\'b'", ""},
		{"e'\\\\'", "EString:e'\\\\'", ""},
		{"U&'\\0041'", "UnicodeString:U&'\\0041'", ""},
		{"u&\"a\"", "UnicodeIdent:u&\"a\"", ""},
		{"B'101' X'1F'", "BitString:B'101' BitString:X'1F'", ""},
		{"N'abc'", "String:N'abc'", ""},
		{"'abc", "String:'abc", ReasonUnterminatedString},
		{"$$ a ; b $$", "DollarString:$$ a ; b $$", ""},
		{"$tag$ $$ $tag$", "DollarString:$tag$ $$ $tag$", ""},
		{"$a$ $b$ x $b$ $a$", "DollarString:$a$ $b$ x $b$ $a$", ""},
		{"$a$ abc", "DollarString:$a$ abc", ReasonUnterminatedDollar},
		{"$1 $23", "Param:$1 Param:$23", ""},
		{"foo$bar", "Ident:foo$bar", ""},
		{"$", "Invalid:$", ReasonUnknownStatement},
		{`"Ab""c"`, `QuotedIdent:"Ab""c"`, ""},
		{`"abc`, `QuotedIdent:"abc`, ReasonUnterminatedIdentifier},
		{`""`, `QuotedIdent:""`, ReasonUnknownStatement},
		{"1.5e-3 .5 0x1F 1_000", "Number:1.5e-3 Number:.5 Number:0x1F Number:1_000", ""},
		{"a::int", "Ident:a Op::: Ident:int", ""},
		{"a<>b", "Ident:a Op:<> Ident:b", ""},
		{"1--x", "Number:1 LineComment:--x", ""},
		{"a+/*c*/b", "Ident:a Op:+ BlockComment:/*c*/ Ident:b", ""},
		{"(a,[b]);", "Punct:( Ident:a Punct:, Punct:[ Ident:b Punct:] Punct:) Punct:;", ""},
		{"a.b", "Ident:a Punct:. Ident:b", ""},
		{"\\copy", "Invalid:\\ Ident:copy", ReasonBackslashCommand},
		{"x\x00", "Ident:x Invalid:\x00", ReasonNULByte},
		{"{", "Invalid:{", ReasonUnknownStatement},
		{"DRОP", "Ident:DRОP", ReasonNonASCII},
		{"(()", "Punct:( Punct:( Punct:)", ReasonUnknownStatement},
		{"())", "Punct:( Punct:) Punct:)", ReasonUnknownStatement},
		{"1..2", "Number:1 Punct:. Number:.2", ""},
		{"'x' 'y'", "String:'x' String:'y'", ""},
	}
	if len(cases) < 40 {
		t.Fatalf("the table has %d cases; S11 asks for 40", len(cases))
	}
	for _, c := range cases {
		toks, err := Tokenize(c.in)
		if got := kindsOf(toks); got != c.want {
			t.Errorf("Tokenize(%q)\n got  %s\n want %s", c.in, got, c.want)
		}
		var le *LexError
		switch {
		case c.err == "" && err != nil:
			t.Errorf("Tokenize(%q): unexpected %v", c.in, err)
		case c.err != "" && (!errors.As(err, &le) || le.Reason != c.err):
			t.Errorf("Tokenize(%q): error %v, want %s", c.in, err, c.err)
		}
		checkLossless(t, c.in, toks)
	}
}

func TestTokenNorm(t *testing.T) {
	toks, _ := Tokenize(`DrOp "MiXed""Q" x`)
	if toks[0].Norm != "drop" || toks[2].Norm != `MiXed"Q` || toks[4].Norm != "x" {
		t.Fatalf("norms: %q %q %q", toks[0].Norm, toks[2].Norm, toks[4].Norm)
	}
	toks, _ = Tokenize("f((a))")
	depths := []int{0, 0, 1, 2, 1, 0}
	for i, d := range depths {
		if toks[i].Depth != d {
			t.Errorf("token %d %q depth %d, want %d", i, toks[i].Text, toks[i].Depth, d)
		}
	}
	if Kind(200).String() != "Kind(200)" || Invalid.String() != "Invalid" {
		t.Error("Kind.String")
	}
	if (&LexError{Reason: "x", Offset: 3}).Error() != "sqlguard: x at byte 3" {
		t.Error("LexError.Error")
	}
}

func checkLossless(t testing.TB, in string, toks []Token) {
	t.Helper()
	var b strings.Builder
	prev := 0
	for _, tk := range toks {
		if tk.Start != prev || in[tk.Start:tk.End] != tk.Text {
			t.Fatalf("Tokenize(%q): token %+v breaks the offsets", in, tk)
		}
		prev = tk.End
		b.WriteString(tk.Text)
	}
	if b.String() != in {
		t.Fatalf("Tokenize(%q) is not lossless: %q", in, b.String())
	}
}

func FuzzTokenize(f *testing.F) {
	for _, s := range corpusSeeds(f) {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		toks, _ := Tokenize(s)
		checkLossless(t, s, toks)
		for _, st := range Split(toks) {
			for _, tk := range st.Tokens {
				if tk.Kind == Punct && tk.Text == ";" && tk.Depth == 0 {
					t.Fatalf("a top-level ; inside a statement: %q", s)
				}
			}
		}
		_ = utf8.ValidString(s)
	})
}

// corpusSeeds returns every corpus SQL string (not the generated ones) as fuzz seeds.
func corpusSeeds(t testing.TB) []string {
	c := loadCorpus(t)
	out := make([]string, 0, len(c.Cases))
	for _, k := range c.Cases {
		if k.SQL != nil {
			out = append(out, *k.SQL)
		}
	}
	return out
}
