package identity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"time"
	_ "time/tzdata" // time zones load the same in a distroless image as on a laptop
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/store"
)

// OrgCreateHook runs inside org creation's one SystemTx(org_create) transaction, after the org, its
// owner, the membership and the org.created deed are written (the transaction is scoped to the
// new org). led seals what the hook changes. S12 registers policystore.BootstrapOrg here
// (cmd/deedglass), so no org ever exists without an enforced policy.
type OrgCreateHook func(ctx context.Context, tx pgx.Tx, led *ledger.Ledger, org store.Org) error

// OrgCreateHooks are run in order by CreateOrg.
var OrgCreateHooks []OrgCreateHook

// Org is the org as the API shows it.
type Org struct {
	Object   string     `json:"object"` // "org"
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Slug     string     `json:"slug"`
	Timezone string     `json:"timezone"`
	Status   string     `json:"status"`
	Created  httpx.Time `json:"created_at"`
	// Stop is the Stop all overlay, on GET /v1/org only (S24, additive): the Console's org-stopped
	// banner reads it on load and again on org.stopped and org.started.
	Stop *OrgStopState `json:"stop,omitempty"`
}

// OrgStopState is GET /v1/org's stop: whether Stop all is on, since when, by whom (the person's
// name, else their email, else the actor id) and why.
type OrgStopState struct {
	Stopped   bool        `json:"stopped"`
	StoppedAt *httpx.Time `json:"stopped_at"`
	StoppedBy *string     `json:"stopped_by"`
	Reason    *string     `json:"reason"`
}

// Org name and slug rules (orgs has the same CHECKs).
const maxOrgName = 100

var slugRE = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?$`)

// SlugFor derives a slug from a name: lower case, runs of anything else become one hyphen, at most
// 40 characters. "" when nothing usable is left.
func SlugFor(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
		} else if b.Len() > 0 && !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	return s
}

// ValidSlug reports whether s is a usable org slug.
func ValidSlug(s string) bool { return slugRE.MatchString(s) }

// CheckOrgName trims a name and checks it is 1–100 characters with no control characters.
func CheckOrgName(name string) (string, error) {
	name = strings.TrimSpace(name)
	n := utf8.RuneCountInString(name)
	if n == 0 || n > maxOrgName || strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", errors.New("the org name must be 1 to 100 characters, without line breaks")
	}
	return name, nil
}

// CreatedOrg is what CreateOrg wrote.
type CreatedOrg struct {
	Org        store.Org
	Owner      store.User
	Membership ids.MembershipID
	Deed       string
}

// ErrSlugTaken: another org has this slug.
var ErrSlugTaken = errors.New("identity: the slug is taken")

// CreateOrg creates an org, its first owner (reusing the person when the email exists) and the
// owner's active membership, seals org.created and runs OrgCreateHooks, all in one
// SystemTx(org_create) transaction. It is a DB-direct operator command (deedglass org create).
func CreateOrg(ctx context.Context, db *store.DB, led *ledger.Ledger, name, slug, ownerEmail string) (CreatedOrg, error) {
	var out CreatedOrg
	name, err := CheckOrgName(name)
	if err != nil {
		return out, err
	}
	email, err := NormalizeEmail(ownerEmail)
	if err != nil {
		return out, err
	}
	if slug == "" {
		slug = SlugFor(name)
	}
	if !ValidSlug(slug) {
		return out, errors.New("the slug must be 1 to 40 lower-case letters, digits and hyphens, starting and ending with a letter or digit")
	}
	err = db.SystemTx(ctx, store.ReasonOrgCreate, func(ctx context.Context, tx pgx.Tx) error {
		u, err := store.UpsertUserByEmail(ctx, tx, email, "")
		if err != nil {
			return err
		}
		if u.Status == "disabled" {
			return errors.New("this person's account is disabled")
		}
		o := store.Org{ID: ids.New[ids.OrgKind]()}
		err = tx.QueryRow(ctx, `INSERT INTO deedglass.orgs (id, name, slug) VALUES ($1, $2, $3)
		    RETURNING id, name, slug, status, api_version, timezone, created_at`, o.ID, name, slug).
			Scan(&o.ID, &o.Name, &o.Slug, &o.Status, &o.APIVersion, &o.Timezone, &o.CreatedAt)
		if store.SQLState(err) == store.CodeUniqueViolation {
			return ErrSlugTaken
		}
		if err != nil {
			return err
		}
		mem := ids.New[ids.MembershipKind]()
		if _, err := tx.Exec(ctx, `INSERT INTO deedglass.memberships (id, org_id, user_id, role, status, joined_at)
		    VALUES ($1, $2, $3, 'owner', 'active', pg_catalog.now())`, mem, o.ID, u.ID); err != nil {
			return err
		}
		if err := store.ScopeSystemTxToOrg(ctx, tx, o.ID); err != nil {
			return err
		}
		sum := sha256.Sum256([]byte(name))
		env, err := led.RecordAdmin(ctx, tx, ledger.AdminEvent{Action: ledger.AdminOrgCreated, Actor: operator(), Target: orgRef(o.ID),
			Data: map[string]any{"name_digest": "sha256:" + hex.EncodeToString(sum[:]), "owner": u.ID.String()}})
		if err != nil {
			return err
		}
		for _, h := range OrgCreateHooks {
			if err := h(ctx, tx, led, o); err != nil {
				return err
			}
		}
		out = CreatedOrg{Org: o, Owner: u, Membership: mem, Deed: env.ID}
		return nil
	})
	return out, err
}

// IssuedLink is a one-time link the CLI prints.
type IssuedLink struct {
	Link      string
	Purpose   string // "login" | "invite" (a pending invitee gets an invite link)
	OrgID     ids.OrgID
	OrgName   string
	UserID    ids.UserID
	ExpiresAt time.Time
	Deed      string // login_link.issued, when sealed
}

// IssueLink creates a one-time sign-in link for a member without sending email: deedglass org
// login-link (sealing login_link.issued) and deedglass org create (whose org.created deed covers
// it). org may be zero: the person's landing org.
func (s *Service) IssueLink(ctx context.Context, email string, org ids.OrgID, seal bool) (IssuedLink, error) {
	email, err := NormalizeEmail(email)
	if err != nil {
		return IssuedLink{}, err
	}
	u, err := s.pre.UserByEmail(ctx, email)
	if errors.Is(err, store.ErrNotFound) {
		return IssuedLink{}, ErrNoMember
	}
	if err != nil {
		return IssuedLink{}, err
	}
	if u.Status == "disabled" {
		return IssuedLink{}, errors.New("this person's account is disabled")
	}
	var mstatus string
	if org.IsZero() {
		var ok bool
		if org, ok = landingOrg(u.Memberships); !ok {
			for _, m := range u.Memberships {
				if m.Status == "invited" && m.OrgStatus == "active" {
					org, ok = m.OrgID, true
					break
				}
			}
		}
		if !ok {
			return IssuedLink{}, ErrNoMember
		}
	}
	for _, m := range u.Memberships {
		if m.OrgID == org && m.OrgStatus == "active" {
			mstatus = m.Status
		}
	}
	if mstatus == "" {
		return IssuedLink{}, ErrNoMember
	}
	raw, hash, err := NewToken()
	if err != nil {
		return IssuedLink{}, err
	}
	out := IssuedLink{Purpose: PurposeLogin, OrgID: org, UserID: u.ID, Link: s.ConfirmLink(raw)}
	err = s.db.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		now := s.now().UTC()
		var err error
		if out.OrgName, err = orgName(ctx, tx, org); err != nil {
			return err
		}
		row := tokenRow{id: ids.New[ids.LoginTokenKind](), org: org, user: u.ID, purpose: PurposeLogin, hash: hash, expires: now.Add(LoginTTL)}
		if mstatus == "invited" {
			var mem ids.MembershipID
			if err := tx.QueryRow(ctx, `SELECT id FROM deedglass.memberships WHERE org_id = $1 AND user_id = $2`, org, u.ID).Scan(&mem); err != nil {
				return err
			}
			row.purpose, row.membership, row.expires = PurposeInvite, &mem, now.Add(InviteTTL)
		}
		out.Purpose, out.ExpiresAt = row.purpose, row.expires
		if err := voidTokens(ctx, tx, u.ID, row.purpose, now); err != nil {
			return err
		}
		if err := insertToken(ctx, tx, row); err != nil {
			return err
		}
		if !seal {
			return nil
		}
		env, err := s.led.RecordAdmin(ctx, tx, ledger.AdminEvent{Action: ledger.AdminLoginLinkIssued, Actor: operator(),
			Target: ptrRef(userRef(u.ID)), Data: map[string]any{"purpose": row.purpose, "via": "cli"}})
		if err != nil {
			return err
		}
		out.Deed = env.ID
		return nil
	})
	return out, err
}

// ErrNoMember: no active or invited member of an active org has this address.
var ErrNoMember = errors.New("identity: no member with this email address")

func orgObject(id ids.OrgID, name, slug, tz, status string, created time.Time) Org {
	return Org{Object: "org", ID: id.String(), Name: name, Slug: slug, Timezone: tz, Status: status, Created: httpx.Time(created)}
}

func getOrg(ctx context.Context, tx pgx.Tx, id ids.OrgID) (Org, error) {
	var name, slug, tz, status string
	var created time.Time
	err := tx.QueryRow(ctx, `SELECT name, slug, timezone, status, created_at FROM deedglass.orgs WHERE id = $1`, id).
		Scan(&name, &slug, &tz, &status, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		return Org{}, store.ErrNotFound
	}
	return orgObject(id, name, slug, tz, status, created), err
}

// GetOrg is GET /v1/org.
func (s *Service) GetOrg(ctx context.Context, p *Principal) (Org, error) {
	var o Org
	err := s.db.OrgReadTx(ctx, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		o, err = getOrg(ctx, tx, p.OrgID)
		if err != nil {
			return err
		}
		var (
			at      *time.Time
			by, why *string
		)
		if err := tx.QueryRow(ctx, `SELECT o.stopped_at,
			    COALESCE((SELECT COALESCE(NULLIF(u.display_name, ''), u.email::text) FROM deedglass.users AS u WHERE u.id = o.stopped_by), o.stopped_by),
			    o.stop_reason
			  FROM deedglass.orgs AS o WHERE o.id = $1`, p.OrgID).Scan(&at, &by, &why); err != nil {
			return err
		}
		o.Stop = &OrgStopState{Stopped: at != nil, StoppedAt: optTime(at), StoppedBy: by, Reason: why}
		return nil
	})
	return o, err
}

// OrgPatch is PATCH /v1/org: only the fields sent change.
type OrgPatch struct {
	Name     *string `json:"name"`
	Timezone *string `json:"timezone"`
}

// UpdateOrg changes the org's name or time zone and seals org.updated.
func (s *Service) UpdateOrg(ctx context.Context, p *Principal, in OrgPatch) (Org, *string, error) {
	var fields []string
	if in.Name != nil {
		n, err := CheckOrgName(*in.Name)
		if err != nil {
			return Org{}, nil, httpx.ErrValidation("name", "`name` must be 1 to 100 characters, without line breaks.")
		}
		in.Name = &n
		fields = append(fields, "name")
	}
	if in.Timezone != nil {
		if !ValidTimezone(*in.Timezone) {
			return Org{}, nil, httpx.ErrValidation("timezone",
				"`timezone` must be an IANA time zone name such as Asia/Kolkata or America/New_York.")
		}
		fields = append(fields, "timezone")
	}
	if len(fields) == 0 {
		return Org{}, nil, httpx.ErrValidation("", "Send name, timezone or both.")
	}
	var (
		o Org
		d *string
	)
	err := s.db.OrgTx(ctx, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		cur, err := getOrg(ctx, tx, p.OrgID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE deedglass.orgs SET name = COALESCE($2, name), timezone = COALESCE($3, timezone) WHERE id = $1`,
			p.OrgID, in.Name, in.Timezone); err != nil {
			return err
		}
		if o, err = getOrg(ctx, tx, p.OrgID); err != nil {
			return err
		}
		if o.Name == cur.Name && o.Timezone == cur.Timezone {
			return nil // nothing changed: no deed
		}
		d, err = s.recordAdmin(ctx, tx, ledger.AdminEvent{Action: ledger.AdminOrgUpdated, Actor: userRef(p.UserID), Target: orgRef(p.OrgID),
			Data: map[string]any{"fields": fields, "timezone": o.Timezone}})
		return err
	})
	return o, d, err
}

// ValidTimezone reports whether tz is an IANA zone name Go can load (time.LoadLocation), and not
// "Local".
func ValidTimezone(tz string) bool {
	if tz == "" || tz == "Local" || len(tz) > 64 {
		return false
	}
	_, err := time.LoadLocation(tz)
	return err == nil
}
