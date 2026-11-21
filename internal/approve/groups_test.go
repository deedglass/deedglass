package approve

import (
	"testing"

	"github.com/deedglass/deedglass/internal/ids"
)

// TestApproverGroupsFourEyes tests clinician approver group routing and four-eyes verification (HC-P07).
func TestApproverGroupsFourEyes(t *testing.T) {
	// Setup hospital personnel
	hod := Person{
		UserID: ids.New[ids.UserKind](),
		Email:  "hod.emergency@manipal.edu",
		Name:   "Dr. Emergency HOD",
		Role:   "admin",
		Groups: []string{"clinicians", "emergency-hod", "er-attendings"},
	}
	attending := Person{
		UserID: ids.New[ids.UserKind](),
		Email:  "attending.dr@manipal.edu",
		Name:   "Dr. Attending",
		Role:   "viewer",
		Groups: []string{"clinicians", "er-attendings"},
	}
	resident := Person{
		UserID: ids.New[ids.UserKind](),
		Email:  "resident.dr@manipal.edu",
		Name:   "Dr. Resident",
		Role:   "viewer",
		Groups: []string{"clinicians"},
	}
	pharmacist := Person{
		UserID: ids.New[ids.UserKind](),
		Email:  "pharma@manipal.edu",
		Name:   "Staff Pharmacist",
		Role:   "viewer",
		Groups: []string{"pharmacy"},
	}
	billingAdmin := Person{
		UserID: ids.New[ids.UserKind](),
		Email:  "claims@manipal.edu",
		Name:   "Billing Specialist",
		Role:   "admin",
		Groups: []string{"revenue-cycle"},
	}

	hospitalRoster := []Person{hod, attending, resident, pharmacist, billingAdmin}

	// 1. Single-approver group rule (orders-need-clinician)
	orderSpec := Spec{
		Approvers: []string{"group:clinicians"},
		Required:  1,
		ExpiresIn: 14400, // 4 hours
	}

	if !orderSpec.Eligible(hod) {
		t.Fatal("HOD should be eligible under group:clinicians")
	}
	if !orderSpec.Eligible(attending) {
		t.Fatal("Attending should be eligible under group:clinicians")
	}
	if !orderSpec.Eligible(resident) {
		t.Fatal("Resident should be eligible under group:clinicians")
	}
	if orderSpec.Eligible(pharmacist) {
		t.Fatal("Pharmacist should NOT be eligible under group:clinicians")
	}
	if orderSpec.Eligible(billingAdmin) {
		t.Fatal("Billing admin should NOT be eligible under group:clinicians")
	}

	// 2. High-risk dual clinician four-eyes requirement (required: 2 from group:er-attendings)
	highRiskSpec := Spec{
		Approvers: []string{"group:er-attendings"},
		Required:  2,
		ExpiresIn: 3600,
	}

	eligibleAttendings, count := eligiblePeople(hospitalRoster, highRiskSpec, "", false)
	if count != 2 {
		t.Fatalf("expected 2 eligible attendings, got %d", count)
	}
	if len(eligibleAttendings) != 2 {
		t.Fatalf("expected 2 people returned, got %d", len(eligibleAttendings))
	}

	// 3. Self-countersigning prevention (CH-05 / Rule 8)
	// When action is initiated on behalf of HOD, HOD cannot countersign their own request
	_, countWithSelf := eligiblePeople(hospitalRoster, highRiskSpec, hod.Email, false)
	if countWithSelf != 1 {
		t.Fatalf("when HOD is author, only %d attendings should be eligible (self-countersign denied), got %d", 1, countWithSelf)
	}

	// 4. Combined group and role spec: [group:pharmacy, role:admin]
	chemoSpec := Spec{
		Approvers: []string{"group:pharmacy", "role:admin"},
		Required:  1,
		ExpiresIn: 7200,
	}
	if !chemoSpec.Eligible(pharmacist) {
		t.Fatal("pharmacist must be eligible via group:pharmacy")
	}
	if !chemoSpec.Eligible(hod) {
		t.Fatal("HOD must be eligible via role:admin")
	}
	if !chemoSpec.Eligible(billingAdmin) {
		t.Fatal("billingAdmin must be eligible via role:admin")
	}
	if chemoSpec.Eligible(resident) {
		t.Fatal("resident (viewer, not in pharmacy) must NOT be eligible")
	}
}

// TestResolvePersonGroups verifies that group memberships are correctly matched by email or UserID.
func TestResolvePersonGroups(t *testing.T) {
	groups := map[string][]string{
		"clinicians":    {"doc1@manipal.edu", "doc2@manipal.edu", "usr_1001"},
		"er-attendings": {"doc1@manipal.edu"},
		"on-call":       {"doc2@manipal.edu"},
	}

	// doc1 by email
	uid1 := ids.New[ids.UserKind]()
	g1 := resolvePersonGroups(groups, uid1, "Doc1@manipal.edu")
	if len(g1) != 2 || g1[0] != "clinicians" || g1[1] != "er-attendings" {
		t.Fatalf("unexpected groups for doc1: %v", g1)
	}

	// doc2 by email
	uid2 := ids.New[ids.UserKind]()
	g2 := resolvePersonGroups(groups, uid2, "doc2@manipal.edu")
	if len(g2) != 2 || g2[0] != "clinicians" || g2[1] != "on-call" {
		t.Fatalf("unexpected groups for doc2: %v", g2)
	}

	// user by ID string
	// Parse user kind into ids.UserID
	uid3, err := ids.Parse[ids.UserKind]("usr_1001")
	if err == nil {
		g3 := resolvePersonGroups(groups, uid3, "other@manipal.edu")
		if len(g3) != 1 || g3[0] != "clinicians" {
			t.Fatalf("unexpected groups for usr_1001: %v", g3)
		}
	}

	// Unmapped user gets empty
	uid4 := ids.New[ids.UserKind]()
	g4 := resolvePersonGroups(groups, uid4, "unknown@hospital.test")
	if len(g4) != 0 {
		t.Fatalf("expected empty groups, got %v", g4)
	}
}

// TestMsgNotEligibleGroups verifies error wording when a group approver is required.
func TestMsgNotEligibleGroups(t *testing.T) {
	msg := MsgNotEligible("orders-need-clinician", []string{"group:clinicians"})
	want := `Rule "orders-need-clinician" needs group:clinicians to countersign. Ask one of them.`
	if msg != want {
		t.Fatalf("got %q, want %q", msg, want)
	}

	msgOr := MsgNotEligible("orders-need-clinician", []string{"group:clinicians", "role:admin"})
	wantOr := `Rule "orders-need-clinician" needs group:clinicians or role:admin to countersign. Ask one of them.`
	if msgOr != wantOr {
		t.Fatalf("got %q, want %q", msgOr, wantOr)
	}
}
