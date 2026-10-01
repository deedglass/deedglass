package config

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// maxYAML caps deedglass.yaml. With anchors, aliases and merge keys refused, this defeats YAML bombs.
const maxYAML = 64 << 10

// --- .env (dev only) ---

// readDotEnv reads plain KEY=VALUE lines (optional "export ", optional matching quotes, no
// interpolation). Only DEEDGLASS_* keys are kept; TXV_* keys are kept so checkNames can refuse them.
func (l *loader) readDotEnv() {
	if l.o.FS == nil {
		return
	}
	b, err := fs.ReadFile(l.o.FS, ".env")
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		l.warn(".env", "couldn't be read ("+errText(err)+").")
		return
	}
	l.dotenv = map[string]string{}
	l.dotLines = map[string]int{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" || strings.ContainsAny(k, " \t") {
			l.warn(".env", fmt.Sprintf("line %d isn't KEY=VALUE and was skipped.", n))
			continue
		}
		up := strings.ToUpper(k)
		if !strings.HasPrefix(k, envPrefix) && !strings.HasPrefix(up, "TXV_") {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		l.dotenv[k] = v
		l.dotLines[k] = n
	}
}

// --- deedglass.yaml ---

type yamlValue struct {
	s    string
	line int
}

func yamlName(env string) string { return strings.ToLower(strings.TrimPrefix(env, envPrefix)) }

func yamlNames() []string {
	out := make([]string, len(Keys))
	for i, k := range Keys {
		out[i] = k.YAMLName()
	}
	return out
}

func (l *loader) readYAML() {
	l.yaml = map[string]yamlValue{}
	if l.o.FS == nil {
		return
	}
	name := l.o.ConfigFile
	fi, err := fs.Stat(l.o.FS, name)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		l.problem(name, "couldn't be read ("+errText(err)+").", "Check the file's permissions.")
		return
	}
	if fi.Size() > maxYAML {
		l.problem(name, fmt.Sprintf("is %d bytes; the limit is %d.", fi.Size(), maxYAML), "Keep deedglass.yaml small: it holds settings, not data.")
		return
	}
	if fi.Mode().Perm()&0o004 != 0 {
		l.warn(name, "is readable by every user on this machine. Run chmod 640 "+name+".")
	}
	f, err := l.o.FS.Open(name)
	if err != nil {
		l.problem(name, "couldn't be read ("+errText(err)+").", "Check the file's permissions.")
		return
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxYAML+1))
	if err != nil {
		l.problem(name, "couldn't be read ("+errText(err)+").", "Check the file's permissions.")
		return
	}
	if len(b) > maxYAML {
		l.problem(name, fmt.Sprintf("is over the %d-byte limit.", maxYAML), "Keep deedglass.yaml small: it holds settings, not data.")
		return
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return // empty file
		}
		l.problem(name, "isn't valid YAML ("+yamlErr(err)+").", "Fix the syntax; see deedglass.example.yaml.")
		return
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		l.problem(name, "has more than one YAML document.", "Keep one document; remove the extra ---.")
		return
	}
	if line, what := unsafeYAML(&doc); what != "" {
		l.problem(name, fmt.Sprintf("uses %s on line %d, which deedglass.yaml doesn't allow.", what, line), "Write each value out in full.")
		return
	}
	root := &doc
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return
		}
		root = root.Content[0]
	}
	if root.Kind == yaml.ScalarNode && root.Tag == "!!null" {
		return
	}
	if root.Kind != yaml.MappingNode {
		l.problem(name, "must be a mapping of settings (key: value).", "See deedglass.example.yaml.")
		return
	}
	secs := sections()
	for i := 0; i+1 < len(root.Content); i += 2 {
		k, v := root.Content[i], root.Content[i+1]
		key := k.Value
		if secs[key] {
			l.readSection(name, key, v)
			continue
		}
		if _, known := keyByName[envPrefix+strings.ToUpper(key)]; !known || key != strings.ToLower(key) {
			next := "See deedglass.example.yaml for every key."
			if s := suggest(key, yamlNames()); s != "" {
				next = "Did you mean " + s + "?"
			}
			l.problem(fmt.Sprintf("%s:%d", name, k.Line), fmt.Sprintf("%q isn't a Deedglass setting.", key), next)
			continue
		}
		if _, dup := l.yaml[key]; dup {
			l.problem(fmt.Sprintf("%s:%d", name, k.Line), fmt.Sprintf("%q appears twice.", key), "Keep one.")
			continue
		}
		s, ok := yamlScalar(key, v)
		if !ok {
			l.problem(fmt.Sprintf("%s:%d", name, v.Line), fmt.Sprintf("%q must be a single value.", key), "Write it on one line, for example "+key+": value.")
			continue
		}
		l.yaml[key] = yamlValue{s: s, line: v.Line}
	}
}

// yamlScalar accepts a scalar, or a list of scalars for trusted_proxies.
func yamlScalar(key string, v *yaml.Node) (string, bool) {
	switch v.Kind {
	case yaml.ScalarNode:
		if v.Tag == "!!null" {
			return "", true
		}
		return v.Value, true
	case yaml.SequenceNode:
		if key != "trusted_proxies" {
			return "", false
		}
		parts := make([]string, 0, len(v.Content))
		for _, c := range v.Content {
			if c.Kind != yaml.ScalarNode {
				return "", false
			}
			parts = append(parts, c.Value)
		}
		return strings.Join(parts, ","), true
	}
	return "", false
}

// unsafeYAML finds anchors, aliases and merge keys.
func unsafeYAML(n *yaml.Node) (int, string) {
	if n.Anchor != "" {
		return n.Line, "an anchor (&" + n.Anchor + ")"
	}
	if n.Kind == yaml.AliasNode {
		return n.Line, "an alias (*" + n.Value + ")"
	}
	if n.Tag == "!!merge" || (n.Kind == yaml.ScalarNode && n.Value == "<<" && n.Style == 0) {
		return n.Line, "a merge key (<<)"
	}
	for _, c := range n.Content {
		if line, what := unsafeYAML(c); what != "" {
			return line, what
		}
	}
	return 0, ""
}

func yamlErr(err error) string { return strings.TrimPrefix(err.Error(), "yaml: ") }

// --- .deedglass/dev-secrets.json (dev only) ---

type devSecretsFile struct {
	Object       string `json:"object"`
	VaultKey     string `json:"vault_key"`
	APIKeyPepper string `json:"api_key_pepper"`
	CreatedAt    string `json:"created_at"`
	Note         string `json:"note"`
}

const devDir = ".deedglass"
const devFile = "dev-secrets.json"

// devSecrets fills a missing vault key and pepper in dev from .deedglass/dev-secrets.json,
// creating it with 32 random bytes each when it doesn't exist. Prod never gets here.
func (l *loader) devSecrets(c *Config) {
	needVault, needPepper := !c.VaultKey.IsSet(), !c.APIKeyPepper.IsSet()
	if !needVault && !needPepper {
		return
	}
	const next = "Or set DEEDGLASS_VAULT_KEY and DEEDGLASS_API_KEY_PEPPER in the environment (openssl rand -base64 32)."
	if l.o.Dir == "" {
		l.problem("DEEDGLASS_VAULT_KEY", "isn't set, and dev secrets are turned off here.", next)
		return
	}
	dir := filepath.Join(l.o.Dir, devDir)
	path := filepath.Join(dir, devFile)
	rel := devDir + "/" + devFile
	fi, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.Mkdir(dir, 0o700); err != nil {
			l.problem(devDir+"/", "couldn't be created ("+errText(err)+").", next)
			return
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			l.problem(devDir+"/", "couldn't be set to mode 0700 ("+errText(err)+").", next)
			return
		}
	case err != nil:
		l.problem(devDir+"/", "couldn't be checked ("+errText(err)+").", next)
		return
	case fi.Mode()&fs.ModeSymlink != 0:
		l.problem(devDir+"/", "is a symlink, and Deedglass refuses to follow it for secrets.", "Remove it; Deedglass creates a real directory.")
		return
	case !fi.IsDir():
		l.problem(devDir+"/", "isn't a directory.", "Remove it; Deedglass creates the directory.")
		return
	case fi.Mode().Perm()&0o022 != 0:
		l.problem(devDir+"/", fmt.Sprintf("is writable by other users (mode %04o).", fi.Mode().Perm()), "Run chmod 700 .deedglass.")
		return
	}

	var f devSecretsFile
	fi, err = os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		f, err = l.newDevSecrets(path)
		if err != nil {
			l.problem(rel, "couldn't be written ("+errText(err)+").", next)
			return
		}
		c.devSecrets.generated = true
	case err != nil:
		l.problem(rel, "couldn't be checked ("+errText(err)+").", next)
		return
	case fi.Mode()&fs.ModeSymlink != 0 || !fi.Mode().IsRegular():
		l.problem(rel, "isn't a regular file.", "Delete it; Deedglass creates a new one.")
		return
	case fi.Mode().Perm()&0o077 != 0:
		l.problem(rel, fmt.Sprintf("is readable by other users (mode %04o).", fi.Mode().Perm()), "Run chmod 600 .deedglass/dev-secrets.json.")
		return
	default:
		b, err := os.ReadFile(path)
		if err == nil {
			err = json.Unmarshal(b, &f)
		}
		vn, vok := b64Len(f.VaultKey)
		pn, pok := b64Len(f.APIKeyPepper)
		if err != nil || !vok || vn != 32 || !pok || pn < 32 {
			l.problem(rel, "is damaged.", "Delete it to generate new dev secrets. Dev API keys made with the old pepper stop working.")
			return
		}
	}
	if needVault {
		c.VaultKey = NewSecret(f.VaultKey)
		l.src["DEEDGLASS_VAULT_KEY"] = SourceDevSecrets
		c.devSecrets.used++
	}
	if needPepper {
		c.APIKeyPepper = NewSecret(f.APIKeyPepper)
		l.src["DEEDGLASS_API_KEY_PEPPER"] = SourceDevSecrets
		c.devSecrets.used++
	}
}

func (l *loader) newDevSecrets(path string) (devSecretsFile, error) {
	var v, p [32]byte
	if _, err := io.ReadFull(l.o.Rand, v[:]); err != nil {
		return devSecretsFile{}, err
	}
	if _, err := io.ReadFull(l.o.Rand, p[:]); err != nil {
		return devSecretsFile{}, err
	}
	f := devSecretsFile{
		Object:       "dev_secrets",
		VaultKey:     base64.StdEncoding.EncodeToString(v[:]),
		APIKeyPepper: base64.StdEncoding.EncodeToString(p[:]),
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
		Note:         "Local development only. Deedglass never reads this file in prod.",
	}
	b, _ := json.MarshalIndent(f, "", "  ")
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return devSecretsFile{}, err
	}
	if _, err := out.Write(append(b, '\n')); err != nil {
		out.Close()
		os.Remove(path)
		return devSecretsFile{}, err
	}
	if err := out.Close(); err != nil {
		os.Remove(path)
		return devSecretsFile{}, err
	}
	return f, nil
}

func errText(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

// --- did-you-mean ---

// suggest returns the closest candidate within edit distance 2, or "".
func suggest(name string, candidates []string) string {
	best, bestD := "", 3
	for _, c := range candidates {
		if d := levenshtein(strings.ToUpper(name), strings.ToUpper(c)); d < bestD {
			best, bestD = c, d
		}
	}
	return best
}

func levenshtein(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
