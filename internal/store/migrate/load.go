// Package migrate is Deedglass's embedded, forward-only, checksummed migration runner (ADR-019).
//
// Invariants:
//   - Files are internal/store/migrations/NNNN_name.sql, contiguous from 0001, each with a header
//     comment. Load refuses anything else, and anything that would fight the runner's own
//     transactions (BEGIN/COMMIT/ROLLBACK lines, CONCURRENTLY).
//   - An applied migration never changes: its checksum is stored, and every Up, Status and Verify
//     compares it with the file this binary carries.
//   - There are no down migrations. A bad migration is fixed by a new one.
//   - Up holds a session advisory lock, so two deploys can't migrate at once, and runs each
//     migration in its own transaction with search_path pinned to pg_catalog, pg_temp.
package migrate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Migration is one embedded SQL file.
type Migration struct {
	Version  int
	Name     string // "foundation"
	SQL      string
	Checksum string // "sha256:" + hex(sha256(file bytes))
}

// File returns the file name, for example "0001_foundation.sql".
func (m Migration) File() string { return fmt.Sprintf("%04d_%s.sql", m.Version, m.Name) }

var fileName = regexp.MustCompile(`^(\d{4})_([a-z0-9_]+)\.sql$`)

// LoadError explains why a migration file was refused.
type LoadError struct {
	File   string
	Line   int // 0 when the problem isn't on one line
	Reason string
}

func (e *LoadError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("migration %s line %d: %s", e.File, e.Line, e.Reason)
	}
	if e.File == "" {
		return "migrations: " + e.Reason
	}
	return fmt.Sprintf("migration %s: %s", e.File, e.Reason)
}

// Load reads every *.sql file at the root of fsys, checks it, and returns the migrations in
// version order. Other files (embed.go) are ignored.
func Load(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("migrations: %w", err)
	}
	var ms []Migration
	seen := map[int]string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		m := fileName.FindStringSubmatch(name)
		if m == nil {
			return nil, &LoadError{File: name, Reason: "the name must be NNNN_name.sql, lowercase, with letters, digits and underscores (for example 0010_audit_views.sql)"}
		}
		v, _ := strconv.Atoi(m[1])
		if v == 0 {
			return nil, &LoadError{File: name, Reason: "versions start at 0001"}
		}
		if prev, dup := seen[v]; dup {
			return nil, &LoadError{File: name, Reason: fmt.Sprintf("version %04d is also used by %s; each version appears once", v, prev)}
		}
		seen[v] = name
		b, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("migration %s: %w", name, err)
		}
		if err := check(name, b); err != nil {
			return nil, err
		}
		sum := sha256.Sum256(b)
		ms = append(ms, Migration{Version: v, Name: m[2], SQL: string(b), Checksum: "sha256:" + hex.EncodeToString(sum[:])})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].Version < ms[j].Version })
	for i, m := range ms {
		if want := i + 1; m.Version != want {
			return nil, &LoadError{Reason: fmt.Sprintf("migration %04d is missing (found %s next); versions are contiguous from 0001", want, m.File())}
		}
	}
	return ms, nil
}

var (
	bom        = []byte{0xEF, 0xBB, 0xBF}
	txControl  = regexp.MustCompile(`(?i)^(begin|commit|rollback|start\s+transaction)\b`)
	concurrent = regexp.MustCompile(`(?i)\bconcurrently\b`)
)

// check applies the per-file rules. Transaction-control lines are looked for only outside
// dollar-quoted bodies, strings and comments, because plpgsql bodies legitimately contain BEGIN.
func check(name string, b []byte) error {
	if bytes.HasPrefix(b, bom) {
		return &LoadError{File: name, Line: 1, Reason: "the file starts with a byte-order mark; save it as UTF-8 without one"}
	}
	if i := bytes.IndexByte(b, '\r'); i >= 0 {
		return &LoadError{File: name, Line: 1 + bytes.Count(b[:i], []byte{'\n'}), Reason: "the file has a carriage return; use LF line endings"}
	}
	s := string(b)
	first, _, _ := strings.Cut(s, "\n")
	if want := "-- " + name + ": "; !strings.HasPrefix(first, want) || len(strings.TrimSpace(first)) <= len(want) {
		return &LoadError{File: name, Line: 1, Reason: fmt.Sprintf("the first line must be the header comment %q followed by its purpose", want+"…")}
	}
	for i, line := range strings.Split(s, "\n") {
		if concurrent.MatchString(line) {
			return &LoadError{File: name, Line: i + 1, Reason: "CONCURRENTLY can't run inside the runner's transaction; build large indexes with a documented manual step"}
		}
	}
	for _, start := range statementLineStarts(s) {
		if kw := txControl.FindString(start.text); kw != "" {
			return &LoadError{File: name, Line: start.line, Reason: fmt.Sprintf("%s: the runner owns transactions; remove transaction control from the file", strings.ToUpper(kw))}
		}
	}
	return nil
}

type lineStart struct {
	line int
	text string // the rest of the line from its first non-space character
}

// statementLineStarts returns, for each line that begins in plain SQL (not inside a string, a
// dollar-quoted body or a block comment), the text after its leading whitespace.
func statementLineStarts(s string) []lineStart {
	const (
		plain = iota
		single
		dollar
		block
	)
	var out []lineStart
	state, tag, depth := plain, "", 0
	line, atLineStart := 1, true
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\n' {
			line++
			atLineStart = true
			continue
		}
		if atLineStart && (c == ' ' || c == '\t') {
			continue
		}
		if atLineStart {
			atLineStart = false
			if state == plain {
				end := strings.IndexByte(s[i:], '\n')
				if end < 0 {
					end = len(s) - i
				}
				out = append(out, lineStart{line: line, text: s[i : i+end]})
			}
		}
		switch state {
		case plain:
			switch {
			case c == '-' && i+1 < len(s) && s[i+1] == '-':
				if j := strings.IndexByte(s[i:], '\n'); j >= 0 {
					i += j - 1
				} else {
					i = len(s)
				}
			case c == '/' && i+1 < len(s) && s[i+1] == '*':
				state, depth = block, 1
				i++
			case c == '\'':
				state = single
			case c == '$':
				if m := dollarTag.FindString(s[i:]); m != "" {
					state, tag = dollar, m
					i += len(m) - 1
				}
			}
		case single:
			if c == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					i++
				} else {
					state = plain
				}
			}
		case dollar:
			if c == '$' && strings.HasPrefix(s[i:], tag) {
				state = plain
				i += len(tag) - 1
			}
		case block:
			if c == '/' && i+1 < len(s) && s[i+1] == '*' {
				depth++
				i++
			} else if c == '*' && i+1 < len(s) && s[i+1] == '/' {
				depth--
				i++
				if depth == 0 {
					state = plain
				}
			}
		}
	}
	return out
}

var dollarTag = regexp.MustCompile(`^\$(?:[A-Za-z_][A-Za-z0-9_]*)?\$`)
