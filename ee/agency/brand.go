package agency

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"

	"github.com/deedglass/deedglass/internal/weekly"
)

// Brand file limits (S52 §3.2).
const (
	MaxLogoBytes   = 200 << 10
	MaxBrandFile   = 16 << 10
	MaxNameRunes   = 80
	MaxContactLine = 120
)

// brandFile is brand.yaml: {name, contact_line, logo_file}.
type brandFile struct {
	Name        string `yaml:"name"`
	ContactLine string `yaml:"contact_line"`
	LogoFile    string `yaml:"logo_file"`
}

// BrandError is a brand file the reports refuse; Error is the sentence a person reads.
type BrandError struct{ Why string }

func (e *BrandError) Error() string { return e.Why }

func brandErr(format string, a ...any) error { return &BrandError{Why: fmt.Sprintf(format, a...)} }

// LoadBrand reads brand.yaml. logo_file is read relative to the brand file's folder: a PNG or an
// SVG of at most 200 KB. An SVG with a script, an event handler, a foreign object, an entity or any
// reference outside the file is refused, never rewritten.
func LoadBrand(path string) (*weekly.Brand, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, brandErr("Can't read the brand file %s: %s", filepath.Base(path), plainErr(err))
	}
	if len(raw) > MaxBrandFile {
		return nil, brandErr("The brand file is %d bytes; keep it under %d", len(raw), MaxBrandFile)
	}
	var f brandFile
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, brandErr("The brand file isn't valid: it takes name, contact_line and logo_file (%s)", plainErr(err))
	}
	b := &weekly.Brand{Name: strings.TrimSpace(f.Name), ContactLine: strings.TrimSpace(f.ContactLine)}
	switch {
	case b.Name == "":
		return nil, brandErr("The brand file needs a name: the agency's name, as clients know it")
	case utf8.RuneCountInString(b.Name) > MaxNameRunes || hasControl(b.Name):
		return nil, brandErr("The brand name is one line of at most %d characters", MaxNameRunes)
	case utf8.RuneCountInString(b.ContactLine) > MaxContactLine || hasControl(b.ContactLine):
		return nil, brandErr("contact_line is one line of at most %d characters", MaxContactLine)
	}
	if f.LogoFile == "" {
		return b, nil
	}
	lp := f.LogoFile
	if !filepath.IsAbs(lp) {
		lp = filepath.Join(filepath.Dir(path), lp)
	}
	st, err := os.Stat(lp)
	if err != nil {
		return nil, brandErr("Can't read the logo %s: %s", filepath.Base(lp), plainErr(err))
	}
	if !st.Mode().IsRegular() {
		return nil, brandErr("The logo %s isn't a regular file", filepath.Base(lp))
	}
	if st.Size() > MaxLogoBytes {
		return nil, brandErr("The logo is %d KB; the limit is 200 KB. Export a smaller PNG or SVG", (st.Size()+1023)/1024)
	}
	logo, err := os.ReadFile(lp)
	if err != nil {
		return nil, brandErr("Can't read the logo %s: %s", filepath.Base(lp), plainErr(err))
	}
	mime, err := CheckLogo(logo)
	if err != nil {
		return nil, err
	}
	b.Logo, b.LogoMIME = logo, mime
	return b, nil
}

var pngMagic = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

// svgRefused are the SVG constructs that could run code or fetch something: refused outright.
var svgRefused = []struct {
	re  *regexp.Regexp
	why string
}{
	{regexp.MustCompile(`(?i)<\s*script`), "a script"},
	{regexp.MustCompile(`(?i)\son[a-z]+\s*=`), "an event handler (on…=)"},
	{regexp.MustCompile(`(?i)<\s*foreignObject`), "a foreignObject"},
	{regexp.MustCompile(`(?i)<!\s*(ENTITY|DOCTYPE)`), "a DOCTYPE or entity"},
	{regexp.MustCompile(`(?i)(?:xlink:)?href\s*=\s*["']\s*[^"'#\s]`), "a link outside the file (href)"},
	{regexp.MustCompile(`(?i)url\(\s*["']?\s*[^"'#\s)]`), "a reference outside the file (url())"},
	{regexp.MustCompile(`(?i)@import`), "an @import"},
	{regexp.MustCompile(`(?i)javascript:`), "a javascript: link"},
	{regexp.MustCompile(`(?i)<\s*(iframe|embed|object|audio|video|image)\b`), "an embedded element"},
}

// CheckLogo returns the logo's media type, or why it is refused.
func CheckLogo(b []byte) (string, error) {
	if len(b) == 0 {
		return "", brandErr("The logo file is empty")
	}
	if len(b) > MaxLogoBytes {
		return "", brandErr("The logo is %d KB; the limit is 200 KB. Export a smaller PNG or SVG", (len(b)+1023)/1024)
	}
	if bytes.HasPrefix(b, pngMagic) {
		return "image/png", nil
	}
	if !utf8.Valid(b) {
		return "", brandErr("The logo isn't a PNG or an SVG")
	}
	s := string(b)
	if !regexp.MustCompile(`(?is)^\s*(<\?xml[^>]*\?>\s*)?(<!--.*?-->\s*)*<svg[\s>]`).MatchString(s) {
		if strings.Contains(strings.ToLower(s), "<svg") && regexp.MustCompile(`(?i)<!\s*(ENTITY|DOCTYPE)`).MatchString(s) {
			return "", brandErr("The logo SVG has a DOCTYPE or entity. Remove it and try again")
		}
		return "", brandErr("The logo isn't a PNG or an SVG")
	}
	for _, r := range svgRefused {
		if r.re.MatchString(s) {
			return "", brandErr("The logo SVG has %s. Export it as a plain SVG or a PNG and try again", r.why)
		}
	}
	return "image/svg+xml", nil
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func plainErr(err error) string {
	var pe *os.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	return strings.TrimSpace(err.Error())
}
