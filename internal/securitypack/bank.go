package securitypack

import (
	"bytes"
	_ "embed"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"go.yaml.in/yaml/v3"

	"github.com/deedglass/deedglass/internal/embedgz"
)

// questions.yaml is embedded gzip-compressed (questions.tar.gz, make embed-gz; the image budget).
//
//go:embed questions.tar.gz
var questionsGz []byte

var questionsYAMLOnce = embedgz.MustString(embedgz.NewFiles(questionsGz), "questions.yaml")

// Status is a question's computed answer. The wire values never change; Word is what people read.
type Status string

// The five statuses. Nothing but a rule over evidence produces InPlace.
const (
	InPlace       Status = "in_place"
	Partly        Status = "partly"
	NotSetUp      Status = "not_set_up"
	NotApplicable Status = "not_applicable"
	CantTell      Status = "cant_tell"
)

// Statuses is every status, in the order summaries list them.
var Statuses = []Status{InPlace, Partly, NotSetUp, NotApplicable, CantTell}

// Word is the status as people read it.
func (s Status) Word() string {
	switch s {
	case InPlace:
		return "In place"
	case Partly:
		return "Partly"
	case NotSetUp:
		return "Not set up"
	case NotApplicable:
		return "Not applicable"
	case CantTell:
		return "Can't tell"
	}
	return string(s)
}

// Ref names one public framework item by id (and, for OWASP, its short title). Never its text.
type Ref struct {
	Framework string `json:"framework" yaml:"framework"`
	ID        string `json:"id" yaml:"id"`
	Title     string `json:"title,omitempty" yaml:"title"`
}

// Group is one of the eight groups.
type Group struct {
	ID    string `yaml:"id"`
	Title string `yaml:"title"`
}

// Question is one entry of the bank.
type Question struct {
	ID       string            `yaml:"id"`
	Group    string            `yaml:"group"`
	Question string            `yaml:"question"`
	Needs    []string          `yaml:"needs"`
	Evidence []string          `yaml:"evidence"`
	Answer   map[Status]string `yaml:"answer"`
	Fix      map[Status]string `yaml:"fix"`
	Check    string            `yaml:"check"`
	Refs     []Ref             `yaml:"refs"`
}

// Bank is the parsed question bank.
type Bank struct {
	V         int        `yaml:"v"`
	Groups    []Group    `yaml:"groups"`
	Questions []Question `yaml:"questions"`
}

// GroupTitle is a group's title ("" for an unknown id).
func (b *Bank) GroupTitle(id string) string {
	for _, g := range b.Groups {
		if g.ID == id {
			return g.Title
		}
	}
	return ""
}

var (
	bankOnce sync.Once
	bank     *Bank
	bankErr  error
)

// Questions is the embedded bank, parsed and checked once.
func Questions() (*Bank, error) {
	bankOnce.Do(func() { bank, bankErr = ParseBank([]byte(questionsYAMLOnce())) })
	return bank, bankErr
}

// MaxQuestionWords is the longest a question may be.
const MaxQuestionWords = 20

var reID = regexp.MustCompile(`^[a-z][a-z0-9_]{2,40}$`)

// ParseBank parses and checks a bank: known groups and collectors, a rule for every id, an answer
// for every status the rule can give (checked by the tests), short questions, refs by id only.
func ParseBank(src []byte) (*Bank, error) {
	dec := yaml.NewDecoder(bytes.NewReader(src))
	dec.KnownFields(true)
	var b Bank
	if err := dec.Decode(&b); err != nil {
		return nil, fmt.Errorf("securitypack: questions.yaml: %w", err)
	}
	if b.V != 1 {
		return nil, fmt.Errorf("securitypack: questions.yaml v %d, want 1", b.V)
	}
	groups := map[string]bool{}
	for _, g := range b.Groups {
		groups[g.ID] = true
	}
	known := map[string]bool{}
	for _, c := range Collectors {
		known[c] = true
	}
	seen := map[string]bool{}
	for _, q := range b.Questions {
		switch {
		case !reID.MatchString(q.ID) || seen[q.ID]:
			return nil, fmt.Errorf("securitypack: question id %q is invalid or repeated", q.ID)
		case !groups[q.Group]:
			return nil, fmt.Errorf("securitypack: %s: unknown group %q", q.ID, q.Group)
		case rules[q.ID] == nil:
			return nil, fmt.Errorf("securitypack: %s has no status rule", q.ID)
		case len(strings.Fields(q.Question)) > MaxQuestionWords:
			return nil, fmt.Errorf("securitypack: %s: the question is longer than %d words", q.ID, MaxQuestionWords)
		case q.Check == "" || len(q.Needs) == 0 || len(q.Answer) == 0:
			return nil, fmt.Errorf("securitypack: %s needs a check, needs and answers", q.ID)
		}
		for _, n := range q.Needs {
			if !known[n] {
				return nil, fmt.Errorf("securitypack: %s needs unknown collector %q", q.ID, n)
			}
		}
		for s := range q.Answer {
			if s == CantTell || s.Word() == string(s) {
				return nil, fmt.Errorf("securitypack: %s: answer for status %q", q.ID, s)
			}
		}
		for s := range q.Fix {
			if s != Partly && s != NotSetUp {
				return nil, fmt.Errorf("securitypack: %s: a fix is only for partly and not_set_up (got %q)", q.ID, s)
			}
		}
		for _, r := range q.Refs {
			if r.Framework == "" || r.ID == "" {
				return nil, fmt.Errorf("securitypack: %s: a ref needs a framework and an id", q.ID)
			}
		}
		seen[q.ID] = true
	}
	return &b, nil
}
