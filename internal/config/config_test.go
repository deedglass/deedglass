package config

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

var update = flag.Bool("update", false, "rewrite the key table in docs/CONFIG.md")

// 32 bytes and 48 bytes, base64.
const (
	key32 = "q83vEjRWeJq83vEjRWeJq83vEjRWeJq83vEjRWeJq80="
	key48 = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8gISIjJCUmJygpKissLS4v"
)

func prodEnv(extra map[string]string) map[string]string {
	m := map[string]string{
		"DEEDGLASS_ENV":            "prod",
		"DEEDGLASS_BASE_URL":       "https://deedglass.example.com",
		"DEEDGLASS_VAULT_KEY":      key32,
		"DEEDGLASS_API_KEY_PEPPER": key48,
	}
	for k, v := range extra {
		if v == "-" {
			delete(m, k)
			continue
		}
		m[k] = v
	}
	return m
}

type tc struct {
	name     string
	env      map[string]string
	files    fstest.MapFS
	check    func(t *testing.T, c *Config, ws []Warning)
	problems []string // substrings, each must appear in some problem (key + message)
	warnings []string
}

func run(t *testing.T, c tc) {
	t.Helper()
	dir := t.TempDir()
	fsys := c.files
	if fsys == nil {
		fsys = fstest.MapFS{}
	}
	cfg, ws, err := Load(LoadOptions{Env: c.env, FS: fsys, Dir: dir})
	var ps *Problems
	if len(c.problems) > 0 {
		if !errors.As(err, &ps) || cfg != nil {
			t.Fatalf("want problems %v, got err=%v cfg=%v", c.problems, err, cfg != nil)
		}
		all := ps.Error()
		for _, want := range c.problems {
			if !strings.Contains(all, want) {
				t.Errorf("problem %q not found in:\n%s", want, all)
			}
		}
	} else if err != nil {
		t.Fatalf("unexpected problems: %v", err)
	}
	var wtext []string
	for _, w := range ws {
		wtext = append(wtext, w.String())
	}
	for _, want := range c.warnings {
		if !strings.Contains(strings.Join(wtext, "\n"), want) {
			t.Errorf("warning %q not found in %q", want, wtext)
		}
	}
	if c.check != nil && cfg != nil {
		c.check(t, cfg, ws)
	}
}

func TestLoad(t *testing.T) {
	cases := []tc{
		{name: "dev with an empty env is valid and generates dev secrets", env: map[string]string{},
			check: func(t *testing.T, c *Config, ws []Warning) {
				if c.Env != Dev || c.Listen != "127.0.0.1:8080" || c.BaseURL.String() != "http://127.0.0.1:8080" ||
					c.LogLevel != slog.LevelInfo || c.Signup != "invite" || c.SlotLagAlertMB != 512 || c.AllowPrivateNetworks {
					t.Errorf("defaults: %+v", c)
				}
				if !c.VaultKey.IsSet() || !c.APIKeyPepper.IsSet() {
					t.Error("dev secrets not filled")
				}
				if used, gen := c.DevSecrets(); used != 2 || !gen {
					t.Errorf("DevSecrets() = %d, %v", used, gen)
				}
				if c.Source("DEEDGLASS_VAULT_KEY") != SourceDevSecrets || c.Source("DEEDGLASS_LISTEN") != SourceDefault ||
					c.Source("DEEDGLASS_SEAL_KEY_FILE") != SourceNotSet {
					t.Error("sources")
				}
				if len(ws) != 0 {
					t.Errorf("warnings %v", ws)
				}
			}},
		{name: "a valid prod config", env: prodEnv(map[string]string{
			"DEEDGLASS_DATABASE_URL":    "postgres://app:pw@db.internal:5432/deedglass?sslmode=verify-full",
			"DEEDGLASS_TRUSTED_PROXIES": "10.0.0.0/8, 192.168.1.7/32",
			"DEEDGLASS_LOG_LEVEL":       "warn",
			"DEEDGLASS_SIGNUP":          "closed",
			"DEEDGLASS_RESEND_API_KEY":  "re_123",
			"DEEDGLASS_MAIL_FROM":       "Deedglass <signin@deedglass.com>",
			"DEEDGLASS_SEAL_KEY_FILE":   "/etc/deedglass/seal.key",
		}), check: func(t *testing.T, c *Config, _ []Warning) {
			if c.Env != Prod || len(c.TrustedProxies) != 2 || c.LogLevel != slog.LevelWarn || c.Signup != "closed" {
				t.Errorf("%+v", c)
			}
			if used, _ := c.DevSecrets(); used != 0 {
				t.Error("prod must not use dev secrets")
			}
			if err := c.Require(NeedDatabase | NeedSealKey | NeedVault | NeedPepper | NeedPublicBaseURL); err != nil {
				t.Error(err)
			}
		}},
		{name: "prod with no vault key", env: prodEnv(map[string]string{"DEEDGLASS_VAULT_KEY": "-"}),
			problems: []string{"DEEDGLASS_VAULT_KEY isn't set. Generate one with `openssl rand -base64 32`"}},
		{name: "prod with nothing reports all three problems at once", env: map[string]string{"DEEDGLASS_ENV": "prod"},
			problems: []string{"3 problems in the prod config",
				"DEEDGLASS_BASE_URL must use https in prod (got http://127.0.0.1:8080). Set it to this server's public https URL.",
				"DEEDGLASS_VAULT_KEY isn't set.",
				"DEEDGLASS_API_KEY_PEPPER isn't set. Generate one the same way, and keep it stable"}},
		{name: "prod with an http base URL", env: prodEnv(map[string]string{"DEEDGLASS_BASE_URL": "http://deedglass.example.com"}),
			problems: []string{"must use https in prod"}},
		{name: "prod sslmode=disable to a remote host", env: prodEnv(map[string]string{"DEEDGLASS_DATABASE_URL": "postgres://a:b@db.example.com/x?sslmode=disable"}),
			problems: []string{"DEEDGLASS_DATABASE_URL has sslmode=disable, and prod needs require or verify-full"}},
		{name: "prod no sslmode to a remote host", env: prodEnv(map[string]string{"DEEDGLASS_DATABASE_URL": "postgres://a@db.example.com/x"}),
			problems: []string{"has no sslmode"}},
		{name: "prod migrate URL needs sslmode too", env: prodEnv(map[string]string{"DEEDGLASS_MIGRATE_DATABASE_URL": "postgres://m:p@db.example.com/x?sslmode=disable"}),
			problems: []string{"DEEDGLASS_MIGRATE_DATABASE_URL has sslmode=disable, and prod needs require or verify-full"}},
		{name: "prod sslmode=disable to 127.0.0.1 is fine", env: prodEnv(map[string]string{"DEEDGLASS_DATABASE_URL": "postgres://a:b@127.0.0.1:5432/x?sslmode=disable"})},
		{name: "prod over a Unix socket is fine", env: prodEnv(map[string]string{"DEEDGLASS_DATABASE_URL": "postgres:///x?host=/var/run/postgresql"})},
		{name: "a typo warns in dev", env: map[string]string{"DEEDGLASS_DATABSE_URL": "x"},
			warnings: []string{"DEEDGLASS_DATABSE_URL isn't a Deedglass setting. Did you mean DEEDGLASS_DATABASE_URL?"}},
		{name: "a typo is an error in prod", env: prodEnv(map[string]string{"DEEDGLASS_DATABSE_URL": "x"}),
			problems: []string{"DEEDGLASS_DATABSE_URL isn't a Deedglass setting. Did you mean DEEDGLASS_DATABASE_URL?"}},
		{name: "an unknown name with no close match", env: map[string]string{"DEEDGLASS_FROBNICATE": "1"},
			warnings: []string{"DEEDGLASS_FROBNICATE isn't a Deedglass setting. See docs/CONFIG.md for the list."}},
		{name: "TXV_ENV is an error", env: map[string]string{"TXV_ENV": "dev"},
			problems: []string{"TXV_ENV is from the old name. Rename it to DEEDGLASS_ENV."}},
		{name: "TXV_PG maps to its tooling name", env: map[string]string{"TXV_PG": "18"},
			problems: []string{"TXV_PG is from the old name. Rename it to DEEDGLASS_IT_PG."}},
		{name: "tooling names are ignored", env: map[string]string{"DEEDGLASS_IT_PG": "18", "DEEDGLASS_DEV_PG_PASSWORD": "x", "DEEDGLASS_REPO_PUBLIC": "1"},
			check: func(t *testing.T, _ *Config, ws []Warning) {
				if len(ws) != 0 {
					t.Errorf("warnings %v", ws)
				}
			}},
		{name: "a YAML alias is refused", files: fstest.MapFS{"deedglass.yaml": {Data: []byte("listen: &l 127.0.0.1:9090\nbase_url: *l\n")}},
			problems: []string{"uses an anchor (&l) on line 1"}},
		{name: "a YAML merge key is refused", files: fstest.MapFS{"deedglass.yaml": {Data: []byte("<<: {listen: x}\n")}},
			problems: []string{"a merge key (<<)"}},
		{name: "a 65 KiB YAML file is refused", files: fstest.MapFS{"deedglass.yaml": {Data: bytes.Repeat([]byte("# padding\n"), 6656)}},
			problems: []string{"the limit is 65536"}},
		{name: "an unknown YAML key gets did-you-mean", files: fstest.MapFS{"deedglass.yaml": {Data: []byte("lisen: 127.0.0.1:9090\n")}},
			problems: []string{`deedglass.yaml:1 "lisen" isn't a Deedglass setting. Did you mean listen?`}},
		{name: "a secret in YAML is refused in prod", env: prodEnv(map[string]string{"DEEDGLASS_VAULT_KEY": "-"}),
			files:    fstest.MapFS{"deedglass.yaml": {Data: []byte("vault_key: " + key32 + "\n")}},
			problems: []string{"DEEDGLASS_VAULT_KEY is a secret, and prod refuses secrets in deedglass.yaml"}},
		{name: "a secret in YAML warns in dev", files: fstest.MapFS{"deedglass.yaml": {Data: []byte("api_key_pepper: " + key48 + "\n")}},
			warnings: []string{"DEEDGLASS_API_KEY_PEPPER is a secret in deedglass.yaml"},
			check: func(t *testing.T, c *Config, _ []Warning) {
				if c.Source("DEEDGLASS_API_KEY_PEPPER") != SourceYAML {
					t.Error("source")
				}
				if used, _ := c.DevSecrets(); used != 1 {
					t.Errorf("only the vault key should come from dev secrets, got %d", used)
				}
			}},
		{name: "a database URL without a password is fine in YAML in prod", env: prodEnv(nil),
			files: fstest.MapFS{"deedglass.yaml": {Data: []byte("database_url: postgres://app@127.0.0.1/x\n")}}},
		{name: "YAML values are used, env wins over YAML", env: map[string]string{"DEEDGLASS_LISTEN": "127.0.0.1:7000"},
			files: fstest.MapFS{"deedglass.yaml": {Data: []byte("listen: 127.0.0.1:9090\nslot_lag_alert_mb: 64\ntrusted_proxies: [10.0.0.0/8, 172.16.0.0/12]\nseal_key_file: ~\n")}},
			check: func(t *testing.T, c *Config, _ []Warning) {
				if c.Listen != "127.0.0.1:7000" || c.SlotLagAlertMB != 64 || len(c.TrustedProxies) != 2 || c.Source("DEEDGLASS_SLOT_LAG_ALERT_MB") != SourceYAML {
					t.Errorf("%+v", c)
				}
			}},
		{name: ".env versus the real env: the real env wins", env: map[string]string{"DEEDGLASS_LISTEN": "127.0.0.1:1111"},
			files: fstest.MapFS{".env": {Data: []byte("# local\nexport DEEDGLASS_LISTEN=\"127.0.0.1:2222\"\nDEEDGLASS_LOG_LEVEL='debug'\nOTHER=1\n")}},
			check: func(t *testing.T, c *Config, _ []Warning) {
				if c.Listen != "127.0.0.1:1111" || c.Source("DEEDGLASS_LISTEN") != SourceEnv {
					t.Errorf("listen %s from %s", c.Listen, c.Source("DEEDGLASS_LISTEN"))
				}
				if c.LogLevel != slog.LevelDebug || c.Source("DEEDGLASS_LOG_LEVEL") != SourceDotEnv {
					t.Errorf("log level from .env: %v %s", c.LogLevel, c.Source("DEEDGLASS_LOG_LEVEL"))
				}
			}},
		{name: ".env is ignored in prod", env: prodEnv(nil),
			files:    fstest.MapFS{".env": {Data: []byte("DEEDGLASS_ALLOW_PRIVATE_NETWORKS=true\n")}},
			warnings: []string{".env is ignored in prod"},
			check: func(t *testing.T, c *Config, _ []Warning) {
				if c.AllowPrivateNetworks {
					t.Error(".env leaked into prod")
				}
			}},
		{name: "prod from .env is refused", files: fstest.MapFS{".env": {Data: []byte("DEEDGLASS_ENV=prod\n")}},
			problems: []string{"DEEDGLASS_ENV is prod in .env, and prod ignores .env."}},
		{name: "a TXV_ name in .env is an error", files: fstest.MapFS{".env": {Data: []byte("TXV_DATABASE_URL=x\n")}},
			problems: []string{"TXV_DATABASE_URL is from the old name. Rename it to DEEDGLASS_DATABASE_URL."}},
		{name: "a malformed .env line warns", files: fstest.MapFS{".env": {Data: []byte("NOT A LINE\nDEEDGLASS_TYPO=1\n")}},
			warnings: []string{".env line 1 isn't KEY=VALUE", "DEEDGLASS_TYPO isn't a Deedglass setting (in .env)."}},
		{name: "prod refuses private networks", env: prodEnv(map[string]string{"DEEDGLASS_ALLOW_PRIVATE_NETWORKS": "true"}),
			problems: []string{"DEEDGLASS_ALLOW_PRIVATE_NETWORKS is dev only."}},
		{name: "bad values are all reported", env: map[string]string{
			"DEEDGLASS_ENV": "staging", "DEEDGLASS_LISTEN": "8080", "DEEDGLASS_BASE_URL": "ftp://x",
			"DEEDGLASS_VAULT_KEY": "c2hvcnQ=", "DEEDGLASS_API_KEY_PEPPER": "!!", "DEEDGLASS_LOG_LEVEL": "loud",
			"DEEDGLASS_SIGNUP": "open", "DEEDGLASS_RESEND_API_KEY": "sk_x", "DEEDGLASS_ANTHROPIC_API_KEY": "sk-x",
			"DEEDGLASS_TRUSTED_PROXIES": "10.0.0.1", "DEEDGLASS_ALLOW_PRIVATE_NETWORKS": "maybe",
			"DEEDGLASS_SLOT_LAG_ALERT_MB": "0", "DEEDGLASS_API_KEY": "sk_live_x", "DEEDGLASS_SERVER": "https://u:p@x",
			"DEEDGLASS_DATABASE_URL": "mysql://x",
		}, problems: []string{
			`DEEDGLASS_ENV must be dev or prod (got "staging").`,
			`DEEDGLASS_LISTEN must be host:port`,
			`DEEDGLASS_BASE_URL must be an http or https URL`,
			`DEEDGLASS_VAULT_KEY has a first key of 5 bytes; each key must be exactly 32.`,
			`DEEDGLASS_API_KEY_PEPPER must be base64 of at least 32 bytes (it isn't valid base64).`,
			`DEEDGLASS_LOG_LEVEL must be one of debug, info, warn, error`,
			`DEEDGLASS_SIGNUP must be one of invite, closed`,
			`DEEDGLASS_RESEND_API_KEY doesn't look like a Resend API key`,
			`DEEDGLASS_ANTHROPIC_API_KEY doesn't look like an Anthropic API key`,
			`Write a single address as 10.0.0.1/32.`,
			`DEEDGLASS_ALLOW_PRIVATE_NETWORKS must be true or false`,
			`DEEDGLASS_SLOT_LAG_ALERT_MB must be a whole number from 1 to 1048576`,
			`DEEDGLASS_API_KEY doesn't look like a Deedglass API key`,
			`DEEDGLASS_SERVER must not contain a user name or password.`,
			`DEEDGLASS_DATABASE_URL must be a postgres:// URL.`,
		}},
		{name: "a valid client key", env: map[string]string{
			"DEEDGLASS_API_KEY": "dg_test_Ab3dEf7h_" + strings.Repeat("Z9", 16), "DEEDGLASS_SERVER": "https://api.deedglass.com/",
		}, check: func(t *testing.T, c *Config, _ []Warning) {
			if !c.APIKey.IsSet() || c.Server.String() != "https://api.deedglass.com" {
				t.Errorf("client keys: %v %v", c.APIKey.IsSet(), c.Server)
			}
		}},
		{name: "a list-valued YAML key other than trusted_proxies is refused",
			files: fstest.MapFS{"deedglass.yaml": {Data: []byte("listen: [a, b]\n")}}, problems: []string{`"listen" must be a single value.`}},
		{name: "a duplicated YAML key is refused",
			files: fstest.MapFS{"deedglass.yaml": {Data: []byte("listen: a:1\nlisten: b:2\n")}}, problems: []string{`"listen" appears twice`}},
		{name: "YAML must be a mapping", files: fstest.MapFS{"deedglass.yaml": {Data: []byte("- a\n- b\n")}},
			problems: []string{"must be a mapping of settings"}},
		{name: "two YAML documents are refused", files: fstest.MapFS{"deedglass.yaml": {Data: []byte("listen: a:1\n---\nlisten: b:2\n")}},
			problems: []string{"more than one YAML document"}},
		{name: "invalid YAML", files: fstest.MapFS{"deedglass.yaml": {Data: []byte("listen: [\n")}},
			problems: []string{"isn't valid YAML"}},
		{name: "empty and null YAML are fine", files: fstest.MapFS{"deedglass.yaml": {Data: []byte("")}}},
		{name: "a null YAML document is fine", files: fstest.MapFS{"deedglass.yaml": {Data: []byte("~\n")}}},
		{name: "a world-readable YAML file warns", files: fstest.MapFS{"deedglass.yaml": {Data: []byte("listen: 127.0.0.1:9\n"), Mode: 0o644}},
			warnings: []string{"is readable by every user"}},
		{name: "env from YAML", env: prodEnv(map[string]string{"DEEDGLASS_ENV": "-"}), files: fstest.MapFS{"deedglass.yaml": {Data: []byte("env: prod\n")}},
			check: func(t *testing.T, c *Config, _ []Warning) {
				if c.Env != Prod {
					t.Error("env from YAML")
				}
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { run(t, c) })
	}
}

func TestRequire(t *testing.T) {
	c, _, err := Load(LoadOptions{Env: map[string]string{}, Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	err = c.Require(NeedDatabase | NeedSealKey | NeedVault | NeedPepper | NeedPublicBaseURL)
	var ps *Problems
	if !errors.As(err, &ps) || len(ps.List) != 2 || !strings.Contains(err.Error(), "DEEDGLASS_DATABASE_URL isn't set") ||
		!strings.Contains(err.Error(), "DEEDGLASS_SEAL_KEY_FILE isn't set") {
		t.Fatalf("Require: %v", err)
	}
	empty := &Config{Env: Prod, sources: map[string]Source{"DEEDGLASS_BASE_URL": SourceDefault}}
	if err := empty.Require(NeedVault | NeedPepper | NeedPublicBaseURL); err == nil || !strings.Contains(err.Error(), "3 problems in the prod config") {
		t.Fatalf("Require on an empty config: %v", err)
	}
	if empty.Source("nope") != SourceNotSet {
		t.Error("unknown key source")
	}
}

func TestMigrateURL(t *testing.T) {
	const app = "postgres://deedglass_app:pw@127.0.0.1:5432/deedglass"
	const mig = "postgres://deedglass_migrator:pw@127.0.0.1:5432/deedglass"
	cases := []struct {
		name     string
		env      Env
		db, m    string
		flag     string
		want     string
		fallback bool
		err      string
	}{
		{name: "flag wins", env: Prod, db: app, m: mig, flag: "postgres://x:y@127.0.0.1/z", want: "postgres://x:y@127.0.0.1/z"},
		{name: "bad flag", env: Dev, flag: "mysql://x", err: "--database-url"},
		{name: "migrate url", env: Prod, db: app, m: mig, want: mig},
		{name: "dev falls back", env: Dev, db: app, want: app, fallback: true},
		{name: "prod never falls back", env: Prod, db: app, err: "DEEDGLASS_MIGRATE_DATABASE_URL isn't set. Set it to the migrator role's URL, or pass --database-url."},
		{name: "dev with nothing", env: Dev, err: "DEEDGLASS_MIGRATE_DATABASE_URL isn't set. Set it (or DEEDGLASS_DATABASE_URL in dev)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{Env: tc.env}
			if tc.db != "" {
				c.DatabaseURL = NewSecret(tc.db)
			}
			if tc.m != "" {
				c.MigrateDatabaseURL = NewSecret(tc.m)
			}
			var f Secret
			if tc.flag != "" {
				f = NewSecret(tc.flag)
			}
			got, fb, err := c.MigrateURL(f)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil || got.Reveal() != tc.want || fb != tc.fallback {
				t.Fatalf("got %q %v %v", got.Reveal(), fb, err)
			}
		})
	}
}

func TestDevSecretsFile(t *testing.T) {
	dir := t.TempDir()
	load := func() (*Config, error) {
		c, _, err := Load(LoadOptions{Env: map[string]string{}, Dir: dir})
		return c, err
	}
	c1, err := load()
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, ".deedglass"))
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v %v", fi.Mode(), err)
	}
	fi, err = os.Stat(filepath.Join(dir, ".deedglass", "dev-secrets.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %v %v", fi.Mode(), err)
	}
	c2, err := load()
	if err != nil {
		t.Fatal(err)
	}
	if c1.VaultKey.Reveal() != c2.VaultKey.Reveal() || c1.APIKeyPepper.Reveal() != c2.APIKeyPepper.Reveal() {
		t.Fatal("dev secrets must be stable across loads")
	}
	if _, gen := c2.DevSecrets(); gen {
		t.Fatal("second load must not regenerate")
	}

	// Loose permissions on the file are refused.
	os.Chmod(filepath.Join(dir, ".deedglass", "dev-secrets.json"), 0o644)
	if _, err := load(); err == nil || !strings.Contains(err.Error(), "is readable by other users") {
		t.Fatalf("loose file: %v", err)
	}
	// A damaged file is refused.
	os.WriteFile(filepath.Join(dir, ".deedglass", "dev-secrets.json"), []byte(`{"vault_key":"x"}`), 0o600)
	os.Chmod(filepath.Join(dir, ".deedglass", "dev-secrets.json"), 0o600)
	if _, err := load(); err == nil || !strings.Contains(err.Error(), "is damaged") {
		t.Fatalf("damaged file: %v", err)
	}
	// A group-writable directory is refused.
	os.Chmod(filepath.Join(dir, ".deedglass"), 0o770)
	if _, err := load(); err == nil || !strings.Contains(err.Error(), "is writable by other users") {
		t.Fatalf("loose dir: %v", err)
	}
}

func TestDevSecretsRefusals(t *testing.T) {
	t.Run("a symlinked .deedglass", func(t *testing.T) {
		dir := t.TempDir()
		target := t.TempDir()
		if err := os.Symlink(target, filepath.Join(dir, ".deedglass")); err != nil {
			t.Skip(err)
		}
		_, _, err := Load(LoadOptions{Env: map[string]string{}, Dir: dir})
		if err == nil || !strings.Contains(err.Error(), ".deedglass/ is a symlink") {
			t.Fatalf("got %v", err)
		}
		if entries, _ := os.ReadDir(target); len(entries) != 0 {
			t.Fatal("nothing may be written through the symlink")
		}
	})
	t.Run("a file named .deedglass", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, ".deedglass"), nil, 0o600)
		_, _, err := Load(LoadOptions{Env: map[string]string{}, Dir: dir})
		if err == nil || !strings.Contains(err.Error(), "isn't a directory") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("a symlinked secrets file", func(t *testing.T) {
		dir := t.TempDir()
		os.Mkdir(filepath.Join(dir, ".deedglass"), 0o700)
		os.Symlink("/etc/hosts", filepath.Join(dir, ".deedglass", "dev-secrets.json"))
		_, _, err := Load(LoadOptions{Env: map[string]string{}, Dir: dir})
		if err == nil || !strings.Contains(err.Error(), "isn't a regular file") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("a read-only directory", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores permissions")
		}
		dir := t.TempDir()
		os.Chmod(dir, 0o500)
		t.Cleanup(func() { os.Chmod(dir, 0o700) })
		_, _, err := Load(LoadOptions{Env: map[string]string{}, Dir: dir})
		if err == nil || !strings.Contains(err.Error(), "Or set DEEDGLASS_VAULT_KEY and DEEDGLASS_API_KEY_PEPPER in the environment") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("dev secrets turned off", func(t *testing.T) {
		_, _, err := Load(LoadOptions{Env: map[string]string{}})
		if err == nil || !strings.Contains(err.Error(), "dev secrets are turned off") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("randomness fails", func(t *testing.T) {
		_, _, err := Load(LoadOptions{Env: map[string]string{}, Dir: t.TempDir(), Rand: bytes.NewReader(nil)})
		if err == nil || !strings.Contains(err.Error(), "couldn't be written") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("env keys need no file", func(t *testing.T) {
		dir := t.TempDir()
		_, _, err := Load(LoadOptions{Env: map[string]string{"DEEDGLASS_VAULT_KEY": key32, "DEEDGLASS_API_KEY_PEPPER": key48}, Dir: dir})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(dir, ".deedglass")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatal("no .deedglass/ should be created when both keys are set")
		}
	})
}

func TestPrint(t *testing.T) {
	env := map[string]string{
		"DEEDGLASS_DATABASE_URL": "postgres://deedglass_app:hunter2-canary@127.0.0.1:55432/deedglass?password=also-canary",
		"DEEDGLASS_VAULT_KEY":    key32,
	}
	c, ws, err := Load(LoadOptions{Env: env, Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	var text, js bytes.Buffer
	if err := c.Print(&text, false); err != nil {
		t.Fatal(err)
	}
	if err := c.PrintWarnings(&js, true, append(ws, Warning{"K", "m"})); err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{text.String(), js.String()} {
		if strings.Contains(out, "canary") || strings.Contains(out, key32) {
			t.Fatalf("secret printed:\n%s", out)
		}
	}
	if !strings.Contains(text.String(), "postgres://deedglass_app:••••••••@127.0.0.1:55432/deedglass?password=••••••••") {
		t.Errorf("masked URL:\n%s", text.String())
	}
	if !strings.Contains(text.String(), "DEEDGLASS_SEAL_KEY_FILE           —") {
		t.Errorf("unset row:\n%s", text.String())
	}
	var parsed struct {
		Object string `json:"object"`
		Keys   []Row  `json:"keys"`
	}
	if err := json.Unmarshal(js.Bytes(), &parsed); err != nil || parsed.Object != "config" || len(parsed.Keys) != len(Keys) {
		t.Fatalf("json: %v %s", err, js.String())
	}
	if maskPostgresURL("::") != Redacted {
		t.Error("an unparsable URL must be fully hidden")
	}
}

// TestRedaction uses a canary secret on every path that could print it.
func TestRedaction(t *testing.T) {
	const canary = "canary-7f3a9c"
	s := NewSecret(canary)
	type holder struct {
		Public  Secret
		private Secret
		ptr     *Secret
	}
	h := holder{Public: s, private: s, ptr: &s}
	cfg := &Config{Env: Dev, VaultKey: s, APIKeyPepper: s, DatabaseURL: s, APIKey: s}

	var outs []string
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d", "%T %v"} {
		outs = append(outs, fmt.Sprintf(verb, s), fmt.Sprintf(verb, h), fmt.Sprintf(verb, cfg), fmt.Sprintf(verb, *cfg))
	}
	outs = append(outs, s.String(), s.GoString(), fmt.Sprint(s), fmt.Sprintln(h))
	for _, v := range []any{s, h, cfg} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		outs = append(outs, string(b))
	}
	tb, _ := s.MarshalText()
	outs = append(outs, string(tb))
	for _, handler := range []func(*bytes.Buffer) slog.Handler{
		func(b *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(b, nil) },
		func(b *bytes.Buffer) slog.Handler { return slog.NewTextHandler(b, nil) },
	} {
		var b bytes.Buffer
		l := slog.New(handler(&b))
		l.Info("config", "secret", s, "holder", h, "cfg", cfg, "group", slog.GroupValue(slog.Any("s", s)))
		outs = append(outs, b.String())
	}
	func() {
		defer func() { outs = append(outs, fmt.Sprint(recover())) }()
		panic(s)
	}()
	func() {
		defer func() { outs = append(outs, fmt.Sprint(recover())) }()
		panic(cfg)
	}()
	for _, o := range outs {
		if strings.Contains(o, canary) || strings.Contains(o, "7f3a9c") || strings.Contains(o, fmt.Sprintf("%x", canary)) {
			t.Fatalf("canary leaked: %s", o)
		}
	}
	if s.Reveal() != canary || !s.IsSet() || NewSecret("").IsSet() || (Secret{}).Reveal() != "" {
		t.Fatal("Reveal/IsSet")
	}
	if fmt.Sprintf("%#v", s) != `config.Secret("••••••••")` {
		t.Fatalf("GoString %q", fmt.Sprintf("%#v", s))
	}
}

// TestProblemsNeverEchoSecrets feeds malformed secret values and checks the messages.
func TestProblemsNeverEchoSecrets(t *testing.T) {
	const canary = "canarysecretvalue"
	env := map[string]string{
		"DEEDGLASS_VAULT_KEY":         canary,
		"DEEDGLASS_API_KEY_PEPPER":    canary,
		"DEEDGLASS_RESEND_API_KEY":    canary,
		"DEEDGLASS_ANTHROPIC_API_KEY": canary,
		"DEEDGLASS_API_KEY":           canary,
		"DEEDGLASS_DATABASE_URL":      "mysql://u:" + canary + "@h/db",
		"TXV_SECRET":                  canary,
	}
	_, ws, err := Load(LoadOptions{Env: env, Dir: t.TempDir()})
	if err == nil {
		t.Fatal("want problems")
	}
	if strings.Contains(err.Error(), canary) || strings.Contains(fmt.Sprint(ws), canary) {
		t.Fatalf("a problem repeated a secret: %v", err)
	}
	_, _, err = Load(LoadOptions{Env: prodEnv(map[string]string{"DEEDGLASS_DATABASE_URL": "postgres://u:" + canary + "@db.example.com/x?sslmode=allow"}), Dir: t.TempDir()})
	if err == nil || strings.Contains(err.Error(), canary) {
		t.Fatalf("prod ssl problem: %v", err)
	}
}

func TestSuggest(t *testing.T) {
	cases := map[string]string{
		"DEEDGLASS_DATABSE_URL": "DEEDGLASS_DATABASE_URL",
		"DEEDGLASS_LISTN":       "DEEDGLASS_LISTEN",
		"deedglass_signup":      "DEEDGLASS_SIGNUP",
		"DEEDGLASS_WHATEVER":    "",
	}
	for in, want := range cases {
		if got := suggest(in, envNames()); got != want {
			t.Errorf("suggest(%q) = %q, want %q", in, got, want)
		}
	}
	if levenshtein("", "abc") != 3 || levenshtein("kitten", "sitting") != 3 {
		t.Error("levenshtein")
	}
}

func TestKeyTable(t *testing.T) {
	seen := map[string]bool{}
	for _, k := range Keys {
		if seen[k.Name] || !strings.HasPrefix(k.Name, envPrefix) || k.Doc == "" || k.Owner == "" || k.Commands == "" {
			t.Errorf("bad key %+v", k)
		}
		seen[k.Name] = true
		if k.Kind == KindEnum && !in(k.Enum, k.Default) {
			t.Errorf("%s default %q not in its enum", k.Name, k.Default)
		}
		if isTooling(k.Name) {
			t.Errorf("%s collides with a tooling prefix", k.Name)
		}
		if _, ok := Lookup(k.Name); !ok {
			t.Errorf("Lookup(%s)", k.Name)
		}
	}
	if _, ok := Lookup("DEEDGLASS_NOPE"); ok {
		t.Error("Lookup of an unknown key")
	}
	// Every default must load cleanly in dev.
	if _, _, err := Load(LoadOptions{Env: map[string]string{}, Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
}

func TestDocsTable(t *testing.T) {
	const path = "../../docs/CONFIG.md"
	const begin, end = "<!-- keys:begin (generated from internal/config/keys.go; go test ./internal/config -run TestDocsTable -update) -->\n", "<!-- keys:end -->"
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	i, j := strings.Index(doc, begin), strings.Index(doc, end)
	if i < 0 || j < i {
		t.Fatalf("docs/CONFIG.md needs the markers %q and %q", begin, end)
	}
	want := MarkdownTable()
	if got := doc[i+len(begin) : j]; got != want {
		if *update {
			if err := os.WriteFile(path, []byte(doc[:i+len(begin)]+want+doc[j:]), 0o644); err != nil {
				t.Fatal(err)
			}
			return
		}
		t.Fatal("docs/CONFIG.md key table drifted from keys.go. Run: go test ./internal/config -run TestDocsTable -update")
	}
}

func TestOSOptions(t *testing.T) {
	t.Setenv("DEEDGLASS_LOG_LEVEL", "debug")
	o := OSOptions()
	if o.Env["DEEDGLASS_LOG_LEVEL"] != "debug" || o.Dir != "." || o.FS == nil {
		t.Fatalf("%+v", o)
	}
}

func BenchmarkLoadDefault(b *testing.B) {
	env := map[string]string{"DEEDGLASS_VAULT_KEY": key32, "DEEDGLASS_API_KEY_PEPPER": key48}
	for b.Loop() {
		if _, _, err := Load(LoadOptions{Env: env, FS: fstest.MapFS{}, Dir: ""}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkLoadYAML2KiB(b *testing.B) {
	env := map[string]string{"DEEDGLASS_VAULT_KEY": key32, "DEEDGLASS_API_KEY_PEPPER": key48}
	y := "listen: 127.0.0.1:9090\nlog_level: debug\nslot_lag_alert_mb: 256\ntrusted_proxies: [10.0.0.0/8]\n" +
		strings.Repeat("# a comment line to pad the file to about two kilobytes of text.\n", 30)
	fsys := fstest.MapFS{"deedglass.yaml": {Data: []byte(y), ModTime: time.Now()}}
	for b.Loop() {
		if _, _, err := Load(LoadOptions{Env: env, FS: fsys}); err != nil {
			b.Fatal(err)
		}
	}
}

// TestExampleYAML keeps deedglass.example.yaml in step with the key table: uncommented, it loads
// cleanly, and it mentions every key that may live in the file.
func TestExampleYAML(t *testing.T) {
	b, err := os.ReadFile("../../deedglass.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	keyLine := regexp.MustCompile(`^# ([a-z_]+): `)
	present := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		if m := keyLine.FindStringSubmatch(line); m != nil {
			present[m[1]] = true
			out = append(out, strings.TrimPrefix(line, "# "))
		}
	}
	for _, k := range Keys {
		secret := k.Secret && k.Kind != KindPostgresURL
		if !secret && !present[k.YAMLName()] {
			t.Errorf("deedglass.example.yaml doesn't mention %s", k.YAMLName())
		}
		if secret && present[k.YAMLName()] {
			t.Errorf("deedglass.example.yaml must not show secret %s as a setting", k.YAMLName())
		}
	}
	fsys := fstest.MapFS{"deedglass.yaml": {Data: []byte(strings.Join(out, "\n"))}}
	if _, ws, err := Load(LoadOptions{Env: map[string]string{}, FS: fsys, Dir: t.TempDir()}); err != nil || len(ws) != 0 {
		t.Fatalf("uncommented example: %v %v", err, ws)
	}
}

// TestVaultKeyRing is S16's key-ring table: 1 key, 2 keys, then four precise refusals.
func TestVaultKeyRing(t *testing.T) {
	k1 := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	k2 := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32))
	k31 := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{3}, 31))
	cases := []struct {
		ring    string
		problem string
		ids     int
	}{
		{ring: k1, ids: 1},
		{ring: k1 + "," + k2, ids: 2},
		{ring: k1 + ",!!notbase64", problem: "DEEDGLASS_VAULT_KEY has a second key that isn't base64. Generate a key with `openssl rand -base64 32`."},
		{ring: k31, problem: "DEEDGLASS_VAULT_KEY has a first key of 31 bytes; each key must be exactly 32."},
		{ring: k1 + "," + k1, problem: "DEEDGLASS_VAULT_KEY lists the same key twice (the first and the second, kek:"},
		{ring: strings.Repeat(k1+",", 4) + k2, problem: "DEEDGLASS_VAULT_KEY holds 5 keys; a ring holds at most 4. Run deedglass vault rotate"},
	}
	for _, tc := range cases {
		env := map[string]string{"DEEDGLASS_VAULT_KEY": tc.ring, "DEEDGLASS_API_KEY_PEPPER": key32}
		c, _, err := Load(LoadOptions{Env: env, FS: fstest.MapFS{}})
		if tc.problem != "" {
			if err == nil || !strings.Contains(err.Error(), tc.problem) {
				t.Errorf("%.20s…: err = %v, want %q", tc.ring, err, tc.problem)
			}
			if err != nil && (strings.Contains(err.Error(), k1) || strings.Contains(err.Error(), k31)) {
				t.Errorf("the problem prints a key: %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%v", err)
		}
		ids := c.VaultKeyIDs()
		if len(ids) != tc.ids {
			t.Fatalf("ids = %v", ids)
		}
		var out bytes.Buffer
		if err := c.Print(&out, false); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), k1) || !strings.Contains(out.String(), ids[0]+" (active)") {
			t.Errorf("config print: %s", out.String())
		}
	}
}

// TestEgressAllowCIDRs: private ranges are accepted; anything overlapping metadata, reserved,
// IPv4-embedding or loopback ranges is refused.
func TestEgressAllowCIDRs(t *testing.T) {
	base := map[string]string{"DEEDGLASS_VAULT_KEY": key32, "DEEDGLASS_API_KEY_PEPPER": key32}
	ok := maps.Clone(base)
	ok["DEEDGLASS_EGRESS_ALLOW_CIDRS"] = "10.20.0.0/16, 192.168.4.7/32"
	c, _, err := Load(LoadOptions{Env: ok, FS: fstest.MapFS{}})
	if err != nil || len(c.EgressAllowCIDRs) != 2 {
		t.Fatalf("%v %v", c, err)
	}
	for _, bad := range []string{"0.0.0.0/0", "169.254.0.0/16", "127.0.0.0/8", "::ffff:0:0/96", "10.0.0.1"} {
		env := maps.Clone(base)
		env["DEEDGLASS_EGRESS_ALLOW_CIDRS"] = bad
		if _, _, err := Load(LoadOptions{Env: env, FS: fstest.MapFS{}}); err == nil || !strings.Contains(err.Error(), "DEEDGLASS_EGRESS_ALLOW_CIDRS") {
			t.Errorf("%s: %v", bad, err)
		}
	}
}
