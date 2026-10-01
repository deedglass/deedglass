package sqlguard

// Table and function extraction. It walks every token of a statement, at every depth, and reads
// table names after the keywords that introduce them (S11 §3.5 "tables"). It is a conservative
// reader, not a parser: when it meets something it can't follow it clears TablesComplete.

// fromInsideCall lists call-like keywords whose parentheses may contain FROM that is not a table
// (EXTRACT(year FROM d), SUBSTRING(s FROM 2), TRIM(BOTH 'x' FROM s), OVERLAY, POSITION).
func fromInsideCall(w string) bool {
	switch w {
	case "extract", "substring", "trim", "overlay", "position", "normalize":
		return true
	}
	return false
}

// listEnd reports whether a keyword ends a table list.
func listEnd(w string) bool {
	switch w {
	case "where", "group", "having", "window", "order", "limit", "offset", "fetch", "for", "union", "intersect",
		"except", "returning", "set", "when", "then", "values", "select", "to", "from", "do", "cascade",
		"restrict", "restart", "continue", "as", "with", "default", "inherits", "partition", "tablespace",
		"is", "in", "owner", "rename", "add", "alter", "drop":
		return true
	}
	return false
}

// scan walks the statement once: tables, functions, RETURNING, row locks, nested writes. It
// returns whether the statement calls a write function.
func (a *analyzer) scan(main int, kind string, ddl bool) (writeFn bool) {
	callParen := -1
	for i := 0; i < len(a.sig); i++ {
		t := a.sig[i]
		if t.Kind == Punct {
			if t.Text == "(" {
				call := i == callParen || fromInsideCall(a.kw(i-1))
				if d := t.Depth + 1; d < len(a.frames) {
					a.frames[d] = frame{call: call}
				}
				if k := a.kw(i + 1); isDML(k) && !ddl && !a.isTopSite(i+1) {
					a.sites = append(a.sites, site{kind: k, at: i + 1, nested: true})
				}
			}
			continue
		}
		if a.consumed[i] {
			continue
		}
		if t.Kind == Ident {
			switch t.Norm {
			case "from":
				if a.inCall(t.Depth) || a.kw(i-1) == "distinct" {
					continue
				}
				switch kind {
				case "revoke", "fetch", "move", "grant":
					continue
				case "copy":
					if t.Depth == a.sig[main].Depth {
						continue
					}
				}
				a.tableList(i+1, false, false)
				continue
			case "join":
				a.tableList(i+1, true, false)
				continue
			case "using":
				if !a.punct(i+1, "(") && !ddl {
					a.tableList(i+1, false, false)
				}
				continue
			case "into":
				switch a.kw(i - 1) {
				case "insert", "merge":
					a.tableList(i+1, true, true) // a "(" after the target is its column list
				default:
					if kind == "select" || kind == "explain" {
						j := i + 1
						for {
							w := a.kw(j)
							if w == "temp" || w == "temporary" || w == "unlogged" || w == "table" {
								j++
								continue
							}
							break
						}
						a.tableList(j, true, true)
					}
				}
				continue
			case "update":
				if ddl || a.kw(i+1) == "set" || a.punct(i+1, "(") || a.punct(i+1, ",") || a.kw(i+1) == "on" {
					continue
				}
				switch a.kw(i - 1) {
				case "for", "no", "key", "on", "of", "or", "before", "after", "instead", "do", "then":
					continue
				}
				a.tableList(i+1, true, false)
				continue
			case "truncate":
				if i == main {
					j := i + 1
					if a.kw(j) == "table" {
						j++
					}
					a.tableList(j, false, true)
				}
				continue
			case "table":
				if i == main {
					a.tableList(i+1, true, false)
				}
				continue
			case "lock":
				if i == main {
					j := i + 1
					if a.kw(j) == "table" {
						j++
					}
					a.tableList(j, false, true)
				}
				continue
			case "copy":
				if i == main && !a.punct(i+1, "(") {
					a.tableList(i+1, true, true)
				}
				continue
			case "returning":
				a.f.HasReturning = true
				continue
			case "for":
				if !ddl {
					switch a.kw(i + 1) {
					case "update", "share":
						a.f.LocksRows = true
					case "no", "key":
						if a.kw(i+2) == "key" && a.kw(i+3) == "update" || a.kw(i+2) == "share" {
							a.f.LocksRows = true
						}
					}
				}
				continue
			case "lateral":
				a.f.TablesComplete = false
				continue
			case "rows":
				if a.kw(i+1) == "from" {
					a.f.TablesComplete = false
				}
				continue
			case "begin":
				if a.kw(i+1) == "atomic" {
					// A SQL-standard function body holds ";" at depth 0: we can't split it.
					a.reasons.add(ReasonUnknownStatement)
				}
				continue
			case "with":
				if i != 0 && !a.consumed[i] {
					a.withList(i)
				}
				continue
			}
		}
		if t.Kind == Ident || t.Kind == QuotedIdent || t.Kind == UnicodeIdent {
			end, _ := a.chain(i)
			if a.punct(end, "(") && a.isCall(i, end) {
				name := a.chainName(i, end)
				a.f.Functions = append(a.f.Functions, name)
				if classifyFunction(name) == fnWrite {
					writeFn = true
				}
				callParen = end
			}
			i = end - 1
		}
	}
	return writeFn
}

func (a *analyzer) isTopSite(i int) bool {
	for _, s := range a.sites {
		if s.at == i {
			return true
		}
	}
	return false
}

func (a *analyzer) inCall(depth int) bool { return depth < len(a.frames) && a.frames[depth].call }

// isCall decides whether the name chain [i, end) followed by "(" calls a function.
func (a *analyzer) isCall(i, end int) bool {
	if end == i+1 && a.sig[i].Kind == Ident && notFunc(a.sig[i].Norm) {
		return false
	}
	if i == 0 {
		return true
	}
	p := a.sig[i-1]
	switch p.Kind {
	case Ident:
		switch p.Norm {
		case "as", "function", "procedure", "aggregate", "routine", "trigger", "type", "domain", "table",
			"view", "index", "sequence", "into", "statistics", "operator", "cast", "publication", "on":
			if p.Norm == "on" && a.kw(i-2) != "index" && !a.inDDLIndex(i) {
				return true
			}
			return false
		case "references":
			a.addTable(i, end)
			a.consume(i, end)
			return false
		case "using":
			// CREATE INDEX … USING btree (col): an index method, not a call.
			if k := a.kw(0); k == "create" || k == "alter" {
				return false
			}
		}
		// An identifier right before a name is an alias with a column list: "orders o(a, b)".
		return keyword(p.Norm)
	case QuotedIdent, UnicodeIdent:
		return false
	case Punct:
		return p.Text != ")"
	}
	return true
}

// inDDLIndex reports whether token i follows "CREATE … INDEX … ON".
func (a *analyzer) inDDLIndex(i int) bool {
	return a.kw(0) == "create" && func() bool {
		for j := 1; j < i; j++ {
			if a.kw(j) == "index" {
				return true
			}
		}
		return false
	}()
}

// tableList reads one table (single) or a comma list starting at i. ddl: a name followed by "("
// is a column list, not a function in FROM.
func (a *analyzer) tableList(i int, single, ddl bool) {
	if i >= len(a.sig) {
		return
	}
	d := a.sig[i].Depth
	for i < len(a.sig) {
		if a.kw(i) == "only" {
			i++
		}
		switch {
		case a.kw(i) == "lateral":
			a.f.TablesComplete = false
			i++
			continue
		case a.kw(i) == "rows" && a.kw(i+1) == "from":
			a.f.TablesComplete = false
			return
		case a.punct(i, "("):
			i = a.closeOf(i)
		case a.isName(i) && !(a.sig[i].Kind == Ident && (listEnd(a.sig[i].Norm) || a.sig[i].Norm == "only")):
			end, parts := a.chain(i)
			switch {
			case a.punct(end, "(") && !ddl:
				a.f.TablesComplete = false // a function in FROM: its rows come from code
			case parts >= 3:
				a.f.TablesComplete = false
				a.consume(i, end)
			default:
				a.addTable(i, end)
				a.consume(i, end)
			}
			i = end - 1
		default:
			return
		}
		if single {
			return
		}
		found := false
		for k := i + 1; k < len(a.sig); k++ {
			t := a.sig[k]
			if t.Depth < d {
				return
			}
			if t.Depth > d {
				continue
			}
			if a.punct(k, ",") {
				i, found = k+1, true
				break
			}
			if t.Kind == Ident && listEnd(t.Norm) {
				return
			}
		}
		if !found {
			return
		}
	}
}

// addTable records the name chain [i, end) as schema.table, unless it names a CTE.
func (a *analyzer) addTable(i, end int) {
	parts := (end - i + 1) / 2
	if parts == 1 {
		n := a.sig[i].Norm
		for _, c := range a.cte {
			if c == n {
				return
			}
		}
		a.f.Tables = append(a.f.Tables, a.opts.DefaultSchema+"."+n)
		return
	}
	if parts == 2 {
		a.f.Tables = append(a.f.Tables, a.sig[i].Norm+"."+a.sig[i+2].Norm)
		return
	}
	a.f.TablesComplete = false
}

// skipIfExists skips IF [NOT] EXISTS at j.
func (a *analyzer) skipIfExists(j int) int {
	if a.kw(j) == "if" {
		j++
		if a.kw(j) == "not" {
			j++
		}
		if a.kw(j) == "exists" {
			j++
		}
	}
	return j
}

// relation reads a relation object word at j (TABLE, VIEW, MATERIALIZED VIEW, SEQUENCE, INDEX,
// FOREIGN TABLE) and returns the index after it.
func (a *analyzer) relation(j int) (int, bool) {
	switch a.kw(j) {
	case "table", "view", "sequence", "index":
		return j + 1, true
	case "materialized":
		if a.kw(j+1) == "view" {
			return j + 2, true
		}
	case "foreign":
		if a.kw(j+1) == "table" {
			return j + 2, true
		}
	}
	return j, false
}

// ddlTargets reads the objects a DDL or privilege statement names (S11 §3.5: DDL targets).
func (a *analyzer) ddlTargets(main int, kind string) {
	j := main + 1
	switch kind {
	case "create":
		for {
			switch a.kw(j) {
			case "or", "replace", "temp", "temporary", "unlogged", "global", "local", "unique", "recursive":
				j++
				continue
			}
			break
		}
		switch a.kw(j) {
		case "role", "user", "group":
			return
		case "index":
			for k := j + 1; k < len(a.sig); k++ {
				if a.sig[k].Depth == a.sig[main].Depth && a.kw(k) == "on" {
					a.tableList(k+1, true, true)
					return
				}
			}
			a.f.TablesComplete = false
			return
		}
		if k, ok := a.relation(j); ok {
			a.tableList(a.skipIfExists(k), true, true)
			return
		}
		a.f.TablesComplete = false
	case "drop", "alter", "comment":
		if kind == "comment" {
			if a.kw(j) != "on" {
				a.f.TablesComplete = false
				return
			}
			j++
		}
		switch a.kw(j) {
		case "role", "user", "group", "owned", "system", "default":
			return
		}
		if k, ok := a.relation(j); ok {
			if a.kw(k) == "concurrently" {
				k++
			}
			a.tableList(a.skipIfExists(k), kind != "drop", true)
			return
		}
		a.f.TablesComplete = false
	case "grant", "revoke":
		for k := j; k < len(a.sig); k++ {
			if a.sig[k].Depth != a.sig[main].Depth {
				continue
			}
			switch a.kw(k) {
			case "to", "from":
				return
			case "on":
				k++
				switch a.kw(k) {
				case "table", "sequence":
					a.tableList(k+1, false, true)
				case "all", "function", "functions", "procedure", "procedures", "routine", "routines", "schema",
					"database", "language", "type", "domain", "foreign", "large", "tablespace", "parameter":
					a.f.TablesComplete = false
				default:
					a.tableList(k, false, true)
				}
				return
			}
		}
	case "security_label", "import":
		a.f.TablesComplete = false
	}
}
