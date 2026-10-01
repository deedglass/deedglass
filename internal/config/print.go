package config

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/deedglass/deedglass/internal/vault/keyring"
)

// Row is one line of `deedglass config print`.
type Row struct {
	Name   string `json:"name"`
	Value  string `json:"value"` // secrets are Redacted; unset is ""
	Set    bool   `json:"set"`
	Secret bool   `json:"secret"`
	Client bool   `json:"client"`
	Source Source `json:"source"`
}

// Rows returns the effective config in key-table order, secrets redacted.
func (c *Config) Rows() []Row {
	rows := make([]Row, 0, len(Keys))
	for _, k := range Keys {
		r := Row{Name: k.Name, Secret: k.Secret, Client: k.Client, Source: c.Source(k.Name)}
		v, ok := c.raw[k.Name]
		switch {
		case k.Name == "DEEDGLASS_VAULT_KEY" && c.VaultKey.IsSet():
			// Only the derived key ids, never the keys (S16): kek:3f9a1c2b7d8e (active), kek:… .
			r.Value, r.Set = Redacted, true
			if ids := c.VaultKeyIDs(); len(ids) > 0 {
				r.Value = ids[0] + " (active)"
				if len(ids) > 1 {
					r.Value += ", " + strings.Join(ids[1:], ", ")
				}
			}
		case k.Name == "DEEDGLASS_API_KEY_PEPPER" && c.APIKeyPepper.IsSet():
			r.Value, r.Set = Redacted, true
		case !ok:
		case k.Kind == KindPostgresURL:
			r.Value, r.Set = maskPostgresURL(v), true
		case k.Secret:
			r.Value, r.Set = Redacted, true
		default:
			r.Value, r.Set = v, true
		}
		rows = append(rows, r)
	}
	return rows
}

// Print writes the effective config as an aligned table, or as one JSON object.
func (c *Config) Print(w io.Writer, asJSON bool) error {
	return c.PrintWarnings(w, asJSON, nil)
}

// PrintWarnings is Print with the warnings from Load included in the JSON form.
func (c *Config) PrintWarnings(w io.Writer, asJSON bool, warnings []Warning) error {
	rows := c.Rows()
	if asJSON {
		type warn struct {
			Key     string `json:"key"`
			Message string `json:"message"`
		}
		ws := make([]warn, 0, len(warnings))
		for _, x := range warnings {
			ws = append(ws, warn{x.Key, x.Message})
		}
		used, generated := c.DevSecrets()
		out := struct {
			Object     string `json:"object"`
			Env        Env    `json:"env"`
			Valid      bool   `json:"valid"`
			Keys       []Row  `json:"keys"`
			Warnings   []warn `json:"warnings"`
			DevSecrets struct {
				Used      int  `json:"used"`
				Generated bool `json:"generated"`
			} `json:"dev_secrets"`
		}{Object: "config", Env: c.Env, Valid: true, Keys: rows, Warnings: ws}
		out.DevSecrets.Used, out.DevSecrets.Generated = used, generated
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		return enc.Encode(out)
	}
	nameW, valW := 0, 0
	for _, r := range rows {
		nameW = max(nameW, len(r.Name))
		valW = max(valW, runeLen(displayValue(r)))
	}
	for _, r := range rows {
		v := displayValue(r)
		if _, err := fmt.Fprintf(w, "%s  %s  %s\n", pad(r.Name, nameW), pad(v, valW), r.Source); err != nil {
			return err
		}
	}
	return nil
}

func displayValue(r Row) string {
	if !r.Set {
		return "—"
	}
	return r.Value
}

func runeLen(s string) int { return len([]rune(s)) }

func pad(s string, w int) string { return s + strings.Repeat(" ", max(0, w-runeLen(s))) }

// MarkdownTable renders the key table for docs/CONFIG.md.
func MarkdownTable() string {
	var b strings.Builder
	b.WriteString("| Key | deedglass.yaml | Default | Secret | Needed by | What it does | Added |\n")
	b.WriteString("|---|---|---|---|---|---|---|\n")
	for _, k := range Keys {
		def := "—"
		if k.Default != "" {
			def = "`" + k.Default + "`"
		}
		secret := "no"
		if k.Secret {
			secret = "yes"
		}
		yamlKey := "`" + k.YAMLName() + "`"
		if k.Secret && k.Kind != KindPostgresURL {
			yamlKey += " (dev only)"
		}
		doc := k.Doc
		if k.Client {
			doc = "Client key; `serve` ignores it. " + doc
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s | %s | %s |\n", k.Name, yamlKey, def, secret, k.Commands, doc, k.Owner)
	}
	return b.String()
}

// VaultKeyIDs lists the vault key ring's ids, active first, or nil when no ring is set.
func (c *Config) VaultKeyIDs() []string {
	if !c.VaultKey.IsSet() {
		return nil
	}
	ks, err := keyring.Parse(c.VaultKey.Reveal())
	if err != nil {
		return nil
	}
	return keyring.IDs(ks)
}
