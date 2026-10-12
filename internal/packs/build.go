package packs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/policy"
)

// Result is a pack's compile and test run.
type Result struct {
	Problems []policy.Diagnostic // errors in the policy or its cases (empty when they compiled)
	Test     *policy.TestResult  // nil when the policy didn't compile
}

// OK reports that the policy compiled and every case passed.
func (r *Result) OK() bool { return len(r.Problems) == 0 && r.Test != nil && r.Test.OK }

// Check compiles a pack's policy and runs its cases.
func Check(ctx context.Context, e Entry, files map[string][]byte) *Result {
	var errs []policy.Diagnostic
	keepErrors := func(ds []policy.Diagnostic) {
		for _, d := range ds {
			if d.Severity == policy.SevError {
				errs = append(errs, d)
			}
		}
	}
	c, ds := policy.Compile(files[e.Policy()], policy.Options{File: e.Policy()})
	keepErrors(ds)
	cf, ds := policy.ParseCasesYAML(files[e.CasesFile()], e.CasesFile())
	keepErrors(ds)
	if c == nil || cf == nil || len(errs) > 0 {
		return &Result{Problems: errs}
	}
	return &Result{Test: policy.RunCases(ctx, c, cf, policy.TestOptions{})}
}

// PackFiles returns a pack's files from a checked source, by file name.
func (c *Checked) PackFiles(e Entry) map[string][]byte {
	out := make(map[string][]byte, len(e.Files))
	for f := range e.Files {
		out[f] = c.Source.Files[e.Name+"/"+f]
	}
	return out
}

// BuildIndex writes the index for the pack directories in src (each holding pack.yaml): the
// release tool and the snapshot generator use it. Every pack must compile, pass its cases and
// carry at least MinCases.
func BuildIndex(ctx context.Context, src *Source) (*Index, error) {
	dirs := map[string]bool{}
	for p := range src.Files {
		if i := strings.IndexByte(p, '/'); i > 0 && p[i+1:] == MetaFile {
			dirs[p[:i]] = true
		}
	}
	ix := &Index{Object: ObjectIndex, V: 1, Packs: []Entry{}}
	for _, d := range slices.Sorted(maps.Keys(dirs)) {
		m, err := ParseMeta(src.Files[d+"/"+MetaFile])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", d, err)
		}
		if m.Name != d {
			return nil, fmt.Errorf("%s/pack.yaml names %q; the directory and the name must agree", d, m.Name)
		}
		e := Entry{ID: m.ID, Name: m.Name, Version: m.Version, Tier: m.Tier, Summary: m.Summary, Files: map[string]string{}}
		files := map[string][]byte{}
		for _, f := range []string{MetaFile, ReadmeFile, e.Policy(), e.CasesFile()} {
			b, ok := src.Files[d+"/"+f]
			if !ok {
				return nil, fmt.Errorf("%s: %s is missing", d, f)
			}
			e.Files[f] = deed.Digest(b)
			files[f] = b
		}
		r := Check(ctx, e, files)
		if !r.OK() {
			return nil, fmt.Errorf("%s: the policy doesn't compile or a case fails; run deedglass policy test %s/%s", d, d, e.Policy())
		}
		e.Cases = r.Test.Passed
		if e.Cases < MinCases {
			return nil, fmt.Errorf("%s: %d cases; a pack needs at least %d", d, e.Cases, MinCases)
		}
		e.SHA256 = FilesDigest(e.Files)
		ix.Packs = append(ix.Packs, e)
	}
	if len(ix.Packs) == 0 {
		return nil, errors.New("no pack directories (each needs pack.yaml)")
	}
	return ix, nil
}

// Canonical renders an index as JCS.
func Canonical(ix *Index) ([]byte, error) { return deed.StrictCanonical(ix) }

// Sign stamps the index with the signer's key and the time, and seals it. It returns the new
// index.json and index.seal.json bytes.
func Sign(ix *Index, s deed.Signer, now time.Time) (index, seal []byte, err error) {
	signed := *ix
	signed.KeyID = s.KeyID()
	signed.SignedAt = now.UTC().Format(time.RFC3339)
	index, err = Canonical(&signed)
	if err != nil {
		return nil, nil, err
	}
	sig, err := s.Sign(SealInput(index))
	if err != nil {
		return nil, nil, err
	}
	seal, err = json.Marshal(Seal{Seal: deed.SealPrefix + b64.EncodeToString(sig), KeyID: s.KeyID()})
	if err != nil {
		return nil, nil, err
	}
	return index, append(seal, '\n'), nil
}

// Planned is one file an install would write.
type Planned struct {
	Path    string         `json:"path"`
	Exists  bool           `json:"exists"`
	Same    bool           `json:"same"` // the file already holds these bytes
	Diff    policy.Unified `json:"diff"`
	Content []byte         `json:"-"`
}

// PlanFiles compares a pack's policy and cases with what is on disk (old, by path; nil when the
// file doesn't exist) and returns the two writes in order.
func PlanFiles(e Entry, files map[string][]byte, dir string, old func(path string) ([]byte, bool)) []Planned {
	var out []Planned
	for _, f := range []string{e.Policy(), e.CasesFile()} {
		p := strings.TrimSuffix(dir, "/") + "/" + f
		cur, exists := old(p)
		out = append(out, Planned{Path: p, Exists: exists, Same: exists && bytes.Equal(cur, files[f]),
			Diff: policy.Diff(cur, files[f], 3), Content: files[f]})
	}
	return out
}
