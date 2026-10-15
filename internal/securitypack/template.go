package securitypack

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// fill writes a template with values from ev only. {f} is the value; {f:one|many} is the number
// and the word ("1 key", "3 keys"), or just the chosen form when a form names {…} itself;
// {f?one|many} is the chosen form without the number. A field that isn't in ev is an error, so a
// template can never print something that wasn't computed.
func fill(tpl string, ev Evidence) (string, error) {
	var b strings.Builder
	for i := 0; i < len(tpl); {
		c := tpl[i]
		if c != '{' {
			b.WriteByte(c)
			i++
			continue
		}
		end, err := closing(tpl, i)
		if err != nil {
			return "", err
		}
		out, err := placeholder(tpl[i+1:end], ev)
		if err != nil {
			return "", err
		}
		b.WriteString(out)
		i = end + 1
	}
	return b.String(), nil
}

// closing finds the brace that closes the one at i (one level of nesting).
func closing(s string, i int) (int, error) {
	depth := 0
	for j := i; j < len(s); j++ {
		switch s[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return j, nil
			}
		}
	}
	return 0, fmt.Errorf("securitypack: unclosed { in %q", s)
}

func placeholder(p string, ev Evidence) (string, error) {
	name, forms, sep := p, "", byte(0)
	if k := strings.IndexAny(p, ":?"); k >= 0 {
		name, forms, sep = p[:k], p[k+1:], p[k]
	}
	v, ok := ev[name]
	if !ok {
		return "", fmt.Errorf("securitypack: the template names %q, which the rule didn't compute", name)
	}
	if sep == 0 {
		return display(name, v), nil
	}
	n, ok := v.(int)
	if !ok {
		return "", fmt.Errorf("securitypack: %q picks a word by number but is %T", name, v)
	}
	one, many, ok := splitForms(forms)
	if !ok {
		return "", fmt.Errorf("securitypack: %q needs two forms one|many", name)
	}
	form := many
	if n == 1 {
		form = one
	}
	if strings.ContainsRune(one, '{') || strings.ContainsRune(many, '{') {
		return fill(form, ev)
	}
	if sep == '?' {
		return form, nil
	}
	return display(name, n) + " " + form, nil
}

// splitForms splits one|many at the top-level bar.
func splitForms(s string) (string, string, bool) {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
		case '|':
			if depth == 0 {
				return s[:i], s[i+1:], true
			}
		}
	}
	return "", "", false
}

// display is a value as people read it: counts with separators, *_on dates as "3 Oct 2026".
func display(name string, v any) string {
	switch x := v.(type) {
	case int:
		return formatCount(int64(x))
	case bool:
		if x {
			return "yes"
		}
		return "no"
	case string:
		if strings.HasSuffix(name, "_on") {
			if t, err := time.Parse("2006-01-02", x); err == nil {
				return t.Format("2 Jan 2006")
			}
		}
		return x
	}
	return fmt.Sprint(v)
}

// formatCount writes n with thousands separators (deed.FormatCount's rule).
func formatCount(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}
