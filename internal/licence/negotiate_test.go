package licence

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"
)

// v010Body and v010Check are a copy of the token shape check that ships in v0.1.0 (c070231,
// internal/licence/token.go). Every token the site signs, with or without features_known, must
// pass it: an older install never refuses a newer token (S50 §3.8, "a token the v0.1.0 CLI can't
// read" must never happen). Do not update this copy when token.go changes; it is the old client.
var v010Plans = map[string]bool{PlanReport: true, PlanFounding: true, PlanPro: true}

var v010Known = []string{FeatureScanFull, FeatureScanFix, FeaturePacksPro, FeatureActionPrivate}

func v010Check(b Body) error {
	bad := func(field, why string) error { return fmt.Errorf("%w: %s %s", errBody, field, why) }
	switch {
	case b.Object != ObjectToken:
		return bad("object", `must be "licence_token"`)
	case b.V != 1:
		return bad("v", "must be 1")
	case !reLic.MatchString(b.ID):
		return bad("id", "must be a lic_ id")
	case !v010Plans[b.Plan]:
		return bad("plan", "must be report, founding or pro")
	case !reHash.MatchString(b.KeySHA256):
		return bad("key_sha256", "must be sha256:<64 hex>")
	case !reInst.MatchString(b.Instance):
		return bad("instance", "must be a Dodo licence instance id")
	case !reLK.MatchString(b.KeyID):
		return bad("key_id", "must be an lk_ id")
	case b.GraceDays < 0 || b.GraceDays > 30:
		return bad("grace_days", "must be 0 to 30")
	}
	if _, err := time.Parse(TimeLayout, b.ValidatedAt); err != nil {
		return bad("validated_at", "must be YYYY-MM-DDTHH:MM:SS.mmmZ")
	}
	if b.PeriodEnd != nil {
		if _, err := time.Parse(TimeLayout, *b.PeriodEnd); err != nil {
			return bad("period_end", "must be null or YYYY-MM-DDTHH:MM:SS.mmmZ")
		}
	}
	if len(b.Features) == 0 || len(b.Features) > 32 {
		return bad("features", "must list 1 to 32 features")
	}
	for _, f := range b.Features {
		if !reFeat.MatchString(f) {
			return bad("features", "must be lowercase names (a-z, 0-9, _; 3 to 32 characters)")
		}
	}
	return nil
}

func v010Granted(t Token) []string {
	out := []string{}
	for _, f := range t.Features {
		if slices.Contains(v010Known, f) && !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	return out
}

func TestNegotiateFeatures(t *testing.T) {
	for _, tc := range []struct {
		name    string
		plan    string
		known   []string
		present bool
		want    []string
	}{
		{"old client, report", PlanReport, nil, false, []string{FeatureScanFull, FeatureScanFix, FeaturePacksPro}},
		{"old client, founding", PlanFounding, nil, false, []string{FeatureActionPrivate, FeatureScanFull, FeatureScanFix, FeaturePacksPro}},
		{"old client, pro", PlanPro, nil, false, []string{FeatureActionPrivate, FeatureScanFull, FeatureScanFix, FeaturePacksPro}},
		{"this build, pro", PlanPro, KnownFeatures(), true, []string{FeatureActionPrivate, FeatureScanFull, FeatureScanFix, FeaturePacksPro, FeatureUndoSession}},
		{"this build, founding", PlanFounding, KnownFeatures(), true, []string{FeatureActionPrivate, FeatureScanFull, FeatureScanFix, FeaturePacksPro, FeatureUndoSession}},
		{"this build, report: no undo_session", PlanReport, KnownFeatures(), true, []string{FeatureScanFull, FeatureScanFix, FeaturePacksPro}},
		{"a client that knows only undo_session", PlanPro, []string{FeatureUndoSession}, true, []string{FeatureUndoSession}},
		{"names in the client's order don't reorder", PlanPro, []string{FeatureUndoSession, FeatureScanFull}, true, []string{FeatureScanFull, FeatureUndoSession}},
		{"unknown names only: the v1 list", PlanPro, []string{"agency", "security_pack"}, true, V1PlanFeatures[PlanPro]},
		{"an empty list: the v1 list", PlanPro, []string{}, true, V1PlanFeatures[PlanPro]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := NegotiateFeatures(tc.plan, tc.known, tc.present)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
	// The plan list never changes underneath a caller.
	got := NegotiateFeatures(PlanPro, nil, false)
	got[0] = "changed"
	if V1PlanFeatures[PlanPro][0] == "changed" {
		t.Fatal("NegotiateFeatures returned the plan table itself")
	}
}

// TestV010AcceptsNegotiatedTokens signs every shape the site can sign and reads it with v0.1.0's
// check: accepted, the signature verifies, and v0.1.0 grants exactly the v1 features it knew.
func TestV010AcceptsNegotiatedTokens(t *testing.T) {
	priv, ks := testSigner(t)
	shapes := []struct {
		name    string
		known   []string
		present bool
	}{
		{"absent", nil, false},
		{"this build", KnownFeatures(), true},
		{"v0.1.0's names", v010Known, true},
		{"a later build", append(KnownFeatures(), "security_pack", "agency"), true},
		{"unknown only", []string{"agency"}, true},
	}
	for _, plan := range []string{PlanReport, PlanFounding, PlanPro} {
		for _, sh := range shapes {
			t.Run(plan+"/"+sh.name, func(t *testing.T) {
				b := bodies()[2]
				b.Plan, b.Features = plan, NegotiateFeatures(plan, sh.known, sh.present)
				tok, err := Sign(testKeyID, priv, b)
				if err != nil {
					t.Fatal(err)
				}
				raw, err := tok.Marshal()
				if err != nil {
					t.Fatal(err)
				}
				var back Token
				if err := json.Unmarshal(raw, &back); err != nil {
					t.Fatal(err)
				}
				if err := v010Check(back.Body); err != nil {
					t.Fatalf("v0.1.0 refuses the token: %v", err)
				}
				if err := back.Verify(ks); err != nil {
					t.Fatalf("signature: %v", err)
				}
				for _, f := range v010Granted(back) {
					if !slices.Contains(V1PlanFeatures[plan], f) {
						t.Fatalf("v0.1.0 would grant %s on %s", f, plan)
					}
				}
				if !sh.present && slices.Contains(back.Features, FeatureUndoSession) {
					t.Fatal("a request without features_known got undo_session")
				}
			})
		}
	}
	// And the committed goldens (both request shapes) pass the old check too.
	for _, b := range bodies() {
		tok, err := Sign(testKeyID, priv, b)
		if err != nil {
			t.Fatal(err)
		}
		if err := v010Check(tok.Body); err != nil {
			t.Fatalf("golden %s: %v", b.ID, err)
		}
	}
	if err := v010Check(Body{Object: ObjectToken, V: 1}); !errors.Is(err, errBody) {
		t.Fatal("the copy must still refuse a malformed body")
	}
}

// TestUndoSessionGrantedOnlyByThisBuild: the new feature is known here, granted by founding and
// pro tokens that name it, never by report.
func TestUndoSessionGrantedOnlyByThisBuild(t *testing.T) {
	priv, ks := testSigner(t)
	for _, plan := range []string{PlanReport, PlanFounding, PlanPro} {
		b := bodies()[2]
		b.Plan, b.Features = plan, NegotiateFeatures(plan, KnownFeatures(), true)
		tok, err := Sign(testKeyID, priv, b)
		if err != nil {
			t.Fatal(err)
		}
		if err := tok.Verify(ks); err != nil {
			t.Fatal(err)
		}
		if want := plan != PlanReport; tok.Has(FeatureUndoSession) != want {
			t.Fatalf("%s: Has(undo_session) = %v", plan, !want)
		}
	}
}
