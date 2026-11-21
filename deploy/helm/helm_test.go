package helm_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type ChartMetadata struct {
	APIVersion  string   `yaml:"apiVersion"`
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Version     string   `yaml:"version"`
	AppVersion  string   `yaml:"appVersion"`
	Keywords    []string `yaml:"keywords"`
}

type ValuesConfig struct {
	Env     string `yaml:"env"`
	BaseURL string `yaml:"baseUrl"`
	Phimode struct {
		Enabled bool   `yaml:"enabled"`
		Mode    string `yaml:"mode"`
	} `yaml:"phimode"`
	OIDC struct {
		Enabled      bool   `yaml:"enabled"`
		ProviderType string `yaml:"providerType"`
		RoleMapping  struct {
			DefaultRole string `yaml:"defaultRole"`
			FailClosed  bool   `yaml:"failClosed"`
		} `yaml:"roleMapping"`
	} `yaml:"oidc"`
	SecurityContext struct {
		RunAsNonRoot             bool `yaml:"runAsNonRoot"`
		ReadOnlyRootFilesystem   bool `yaml:"readOnlyRootFilesystem"`
		AllowPrivilegeEscalation bool `yaml:"allowPrivilegeEscalation"`
	} `yaml:"securityContext"`
	NetworkPolicy struct {
		Enabled bool `yaml:"enabled"`
	} `yaml:"networkPolicy"`
}

func TestHelmChartMetadata(t *testing.T) {
	chartPath := filepath.Join("deedglass", "Chart.yaml")
	data, err := os.ReadFile(chartPath)
	if err != nil {
		t.Fatalf("failed to read Chart.yaml: %v", err)
	}

	var meta ChartMetadata
	if err := yaml.Unmarshal(data, &meta); err != nil {
		t.Fatalf("failed to parse Chart.yaml: %v", err)
	}

	if meta.APIVersion != "v2" {
		t.Errorf("expected apiVersion v2, got %q", meta.APIVersion)
	}
	if meta.Name != "deedglass" {
		t.Errorf("expected chart name deedglass, got %q", meta.Name)
	}
	if meta.Version != "0.15.0" {
		t.Errorf("expected chart version 0.15.0, got %q", meta.Version)
	}
	if meta.AppVersion != "0.15.0" {
		t.Errorf("expected appVersion 0.15.0, got %q", meta.AppVersion)
	}
	if len(meta.Keywords) == 0 {
		t.Errorf("expected keywords in Chart.yaml")
	}
}

func TestHelmValuesDefaults(t *testing.T) {
	valuesPath := filepath.Join("deedglass", "values.yaml")
	data, err := os.ReadFile(valuesPath)
	if err != nil {
		t.Fatalf("failed to read values.yaml: %v", err)
	}

	var values ValuesConfig
	if err := yaml.Unmarshal(data, &values); err != nil {
		t.Fatalf("failed to parse values.yaml: %v", err)
	}

	if values.Env != "prod" {
		t.Errorf("expected default env=prod, got %q", values.Env)
	}
	if !values.Phimode.Enabled || values.Phimode.Mode != "digest" {
		t.Errorf("expected phimode enabled with digest mode, got %+v", values.Phimode)
	}
	if !values.SecurityContext.RunAsNonRoot {
		t.Errorf("expected securityContext.runAsNonRoot=true")
	}
	if !values.SecurityContext.ReadOnlyRootFilesystem {
		t.Errorf("expected securityContext.readOnlyRootFilesystem=true")
	}
	if values.SecurityContext.AllowPrivilegeEscalation {
		t.Errorf("expected securityContext.allowPrivilegeEscalation=false")
	}
	if !values.NetworkPolicy.Enabled {
		t.Errorf("expected networkPolicy.enabled=true for P-093 zero outbound telemetry")
	}
	if !values.OIDC.RoleMapping.FailClosed {
		t.Errorf("expected oidc.roleMapping.failClosed=true for enterprise clinical safety")
	}
}

func TestHelmTemplatesExist(t *testing.T) {
	requiredTemplates := []string{
		"_helpers.tpl",
		"configmap.yaml",
		"secret.yaml",
		"deployment.yaml",
		"service.yaml",
		"serviceaccount.yaml",
		"ingress.yaml",
		"networkpolicy.yaml",
		"job-migrate.yaml",
		"tests/test-connection.yaml",
	}

	for _, tmpl := range requiredTemplates {
		p := filepath.Join("deedglass", "templates", tmpl)
		info, err := os.Stat(p)
		if err != nil {
			t.Errorf("missing required Helm template: %s (%v)", p, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("empty Helm template: %s", p)
		}
	}
}

func TestHelmBannedWordsScan(t *testing.T) {
	wordsPath := filepath.Join("..", "..", "scripts", "banned_words.txt")
	wordsBytes, err := os.ReadFile(wordsPath)
	if err != nil {
		t.Fatalf("failed to read %s: %v", wordsPath, err)
	}

	var bannedPhrases []string
	for _, line := range strings.Split(string(wordsBytes), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "@") {
			continue
		}
		bannedPhrases = append(bannedPhrases, strings.ToLower(line))
	}

	err = filepath.Walk("deedglass", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		content := strings.ToLower(string(data))
		for _, b := range bannedPhrases {
			if strings.Contains(content, b) {
				t.Errorf("file %s contains banned phrase %q", path, b)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("filepath.Walk failed: %v", err)
	}
}

func TestValuesSchemaJSON(t *testing.T) {
	schemaPath := filepath.Join("deedglass", "values.schema.json")
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("failed to read values.schema.json: %v", err)
	}

	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("values.schema.json is not valid JSON: %v", err)
	}

	required, ok := schema["required"].([]any)
	if !ok || len(required) == 0 {
		t.Errorf("expected non-empty required fields in values schema")
	}
}

func TestNetworkPolicyZeroWildcardEgress(t *testing.T) {
	netPolPath := filepath.Join("deedglass", "templates", "networkpolicy.yaml")
	content, err := os.ReadFile(netPolPath)
	if err != nil {
		t.Fatalf("failed to read networkpolicy.yaml: %v", err)
	}

	// Must never permit 0.0.0.0/0 egress anywhere in the policy (P-093)
	if strings.Contains(string(content), "0.0.0.0/0") {
		t.Fatalf("network policy violates P-093: contains wildcard 0.0.0.0/0 egress")
	}
}

func TestTemplateBracesBalanced(t *testing.T) {
	templatesDir := filepath.Join("deedglass", "templates")
	err := filepath.Walk(templatesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		opens := strings.Count(string(content), "{{")
		closes := strings.Count(string(content), "}}")
		if opens != closes {
			t.Errorf("template %s has unbalanced delimiters: {{ count=%d, }} count=%d", path, opens, closes)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
