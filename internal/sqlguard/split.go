package sqlguard

// Statement is one statement of a SQL string: its tokens, without the terminating ";".
type Statement struct {
	Tokens []Token // every token, trivia included (a sub-slice of Tokenize's result)
	sig    []int   // indexes into Tokens of the significant (non-trivia) tokens
}

// Empty reports whether the statement has only whitespace and comments.
func (s *Statement) Empty() bool {
	for i := range s.Tokens {
		if !s.Tokens[i].trivia() {
			return false
		}
	}
	return true
}

// Split cuts tokens into statements on ";" at parenthesis depth 0. Strings, comments, quoted
// identifiers and dollar quotes are single tokens, so a ";" inside them never splits. The last
// statement is included even when it is empty, so callers count only non-empty ones.
func Split(toks []Token) []Statement {
	out := make([]Statement, 0, 2)
	start := 0
	for i := range toks {
		t := &toks[i]
		if t.Kind == Punct && t.Text == ";" && t.Depth == 0 {
			out = append(out, Statement{Tokens: toks[start:i]})
			start = i + 1
		}
	}
	out = append(out, Statement{Tokens: toks[start:]})
	return out
}
