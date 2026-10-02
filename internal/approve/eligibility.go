package approve

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/ids"
)

// Who can countersign (S22 §3.6). Eligibility is read live, at request (to count) and at every
// vote: role:admin means an admin or an owner, role:owner an owner, user:<email> that member in any
// role (the policy named them). Only active members of the org count. Agents and keys never do.

// Person is an org member as approvals see them.
type Person struct {
	UserID ids.UserID
	Email  string // never logged, never in a webhook or a deed
	Name   string // display name; the email's local part when none is set
	Role   string // owner | admin | viewer
}

// Spec is a rule's approval spec, as stored on the approval.
type Spec struct {
	Approvers []string `json:"approvers"`
	Required  int      `json:"required"`
	ExpiresIn int64    `json:"expires_in"`
}

func parseSpec(b []byte) Spec {
	var s Spec
	_ = json.Unmarshal(b, &s)
	if len(s.Approvers) == 0 {
		s.Approvers = []string{"role:admin"}
	}
	if s.Required < 1 {
		s.Required = 1
	}
	return s
}

// Eligible reports whether p may countersign under s.
func (s Spec) Eligible(p Person) bool {
	for _, a := range s.Approvers {
		kind, val, _ := strings.Cut(a, ":")
		switch kind {
		case "role":
			switch val {
			case "admin":
				if p.Role == "admin" || p.Role == "owner" {
					return true
				}
			case "owner":
				if p.Role == "owner" {
					return true
				}
			}
		case "user":
			if strings.EqualFold(val, p.Email) {
				return true
			}
		}
	}
	return false
}

// IsSelf reports whether the action runs on p's behalf: Deedglass-On-Behalf-Of names p's usr_ id or
// email (case-insensitive).
func IsSelf(onBehalfOf string, p Person) bool {
	o := strings.TrimSpace(onBehalfOf)
	if o == "" {
		return false
	}
	return o == p.UserID.String() || strings.EqualFold(o, p.Email)
}

// members are the org's active members, in a stable order (role, then email).
func members(ctx context.Context, tx pgx.Tx, org ids.OrgID) ([]Person, error) {
	rows, err := tx.Query(ctx, `SELECT u.id, u.email, COALESCE(u.display_name, ''), m.role
	    FROM deedglass.memberships AS m JOIN deedglass.users AS u ON u.id = m.user_id
	    WHERE m.org_id = $1 AND m.status = 'active' AND u.status <> 'disabled'`, org)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Person, error) {
		var p Person
		err := r.Scan(&p.UserID, &p.Email, &p.Name, &p.Role)
		if p.Name == "" {
			p.Name, _, _ = strings.Cut(p.Email, "@")
		}
		return p, err
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Email < out[j].Email })
	return out, nil
}

// member is one active member of the org, or ok false.
func member(ctx context.Context, tx pgx.Tx, org ids.OrgID, user ids.UserID) (Person, bool, error) {
	var p Person
	err := tx.QueryRow(ctx, `SELECT u.id, u.email, COALESCE(u.display_name, ''), m.role
	    FROM deedglass.memberships AS m JOIN deedglass.users AS u ON u.id = m.user_id
	    WHERE m.org_id = $1 AND m.user_id = $2 AND m.status = 'active' AND u.status <> 'disabled'`, org, user).
		Scan(&p.UserID, &p.Email, &p.Name, &p.Role)
	if err == pgx.ErrNoRows {
		return p, false, nil
	}
	if err != nil {
		return p, false, err
	}
	if p.Name == "" {
		p.Name, _, _ = strings.Cut(p.Email, "@")
	}
	return p, true, nil
}

// eligiblePeople are the members who may vote on an approval with spec s: everyone eligible,
// and how many of them could countersign (the person the action runs for doesn't count unless the
// org allows self-countersign).
func eligiblePeople(all []Person, s Spec, onBehalfOf string, allowSelf bool) (people []Person, canCountersign int) {
	for _, p := range all {
		if !s.Eligible(p) {
			continue
		}
		people = append(people, p)
		if allowSelf || !IsSelf(onBehalfOf, p) {
			canCountersign++
		}
	}
	return people, canCountersign
}

// Settings are the org's approval settings (orgs.settings.approvals), read with defaults. S26 adds
// the Console's form.
type Settings struct {
	EmailLinks      string `json:"email_links"`      // decide (default) | review
	EmailDetail     string `json:"email_detail"`     // summary (default) | full
	SelfCountersign string `json:"self_countersign"` // deny (default) | allow
}

// orgInfo is what approvals read from the org row.
type orgInfo struct {
	Name     string
	TimeZone string
	Settings Settings
}

func readOrg(ctx context.Context, tx pgx.Tx, org ids.OrgID) (orgInfo, error) {
	var (
		o   orgInfo
		raw []byte
	)
	if err := tx.QueryRow(ctx, `SELECT name, timezone, settings FROM deedglass.orgs WHERE id = $1`, org).Scan(&o.Name, &o.TimeZone, &raw); err != nil {
		return o, err
	}
	var s struct {
		Approvals Settings `json:"approvals"`
	}
	_ = json.Unmarshal(raw, &s)
	o.Settings = s.Approvals
	if o.Settings.EmailLinks != "review" {
		o.Settings.EmailLinks = "decide"
	}
	if o.Settings.EmailDetail != "full" {
		o.Settings.EmailDetail = "summary"
	}
	if o.Settings.SelfCountersign != "allow" {
		o.Settings.SelfCountersign = "deny"
	}
	return o, nil
}
