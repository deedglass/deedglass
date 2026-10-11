package ledger

import (
	"context"
	"net/http"
	"regexp"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/httpx"
)

// POST /v1/deeds/files (S38, carried over from S37 §3.9): seal a file's sha256, size, media type
// and label as the admin deed file.sealed. The file itself never reaches the server: the CLI
// (deedglass deeds seal-file) hashes it locally and sends these four fields only.
//
// The deed carries the label as admin.reason and data_digest = sha256 of JCS({bytes, label,
// media_type, sha256}), so deedglass verify --file can check a file against the deed alone: it
// hashes the file, reads the label from the deed, and tries the four media types below.

// MaxFileLabel is the longest label, in characters.
const MaxFileLabel = 120

// MaxFileBytes is the largest size a sealed file may declare (2^53−1, the deed's integer limit).
const MaxFileBytes int64 = 1<<53 - 1

// FileMediaTypes are the media types a sealed file may have, in the order verify --file tries them.
var FileMediaTypes = []string{"application/pdf", "text/markdown", "application/json", "text/plain", "text/html"}

var reFileSHA256 = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// SealFileBody is POST /v1/deeds/files' request.
type SealFileBody struct {
	SHA256    string `json:"sha256"`
	Bytes     *int64 `json:"bytes"`
	MediaType string `json:"media_type"`
	Label     string `json:"label"`
}

// SealedFile is the sealed_file object: what was sealed, and the deed envelope that seals it.
type SealedFile struct {
	Object    string         `json:"object"` // "sealed_file"
	SHA256    string         `json:"sha256"`
	Bytes     int64          `json:"bytes"`
	MediaType string         `json:"media_type"`
	Label     string         `json:"label"`
	Deed      *deed.Envelope `json:"deed"`
}

// FileSealedData is the admin event data of file.sealed; data_digest is the digest of its JCS.
type FileSealedData struct {
	Bytes     int64  `json:"bytes"`
	Label     string `json:"label"`
	MediaType string `json:"media_type"`
	SHA256    string `json:"sha256"`
}

// FileDataDigest is the deed's admin.data_digest for a sealed file with these fields.
func FileDataDigest(d FileSealedData) (string, error) { return deed.DigestValue(d) }

// ValidFileLabel reports whether s is a usable label: 1 to 120 characters of valid UTF-8 with no
// control characters (the deed's reason rules, shorter).
func ValidFileLabel(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	n := 0
	for _, r := range s {
		n++
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == 0x2028 || r == 0x2029 {
			return false
		}
	}
	return n >= 1 && n <= MaxFileLabel
}

// ValidFileMediaType reports whether t is one of FileMediaTypes.
func ValidFileMediaType(t string) bool {
	for _, m := range FileMediaTypes {
		if m == t {
			return true
		}
	}
	return false
}

// CheckSealFile validates a seal-file request; every refusal names its field.
func CheckSealFile(in SealFileBody) error {
	switch {
	case !reFileSHA256.MatchString(in.SHA256):
		return httpx.ErrValidation("sha256", "sha256 must be sha256: and 64 lowercase hex digits, the file's digest. deedglass deeds seal-file computes it for you.")
	case in.Bytes == nil || *in.Bytes < 0 || *in.Bytes > MaxFileBytes:
		return httpx.ErrValidation("bytes", "bytes must be the file's size, a whole number from 0 to 9007199254740991.")
	case !ValidFileMediaType(in.MediaType):
		return httpx.ErrValidation("media_type", "media_type must be application/pdf, text/markdown, application/json or text/plain.")
	case !ValidFileLabel(in.Label):
		return httpx.ErrValidation("label", "label must be 1 to 120 characters with no line breaks or control characters, like \"Full scan report · Acme\".")
	}
	return nil
}

func (h handlers) sealFile(w http.ResponseWriter, r *http.Request) error {
	o, err := org(r)
	if err != nil {
		return err
	}
	p, _ := httpx.PrincipalFrom(r.Context())
	var actor deed.Ref
	switch {
	case p.Kind == httpx.KindUser && !p.UserID.IsZero():
		actor = deed.Ref{Type: "user", ID: p.UserID.String()}
	case p.Kind == httpx.KindAdminKey && !p.KeyID.IsZero():
		actor = deed.Ref{Type: "api_key", ID: p.KeyID.String()}
	default:
		return httpx.ErrAgentKeyForAdmin()
	}
	var in SealFileBody
	if err := httpx.DecodeJSON(r, &in); err != nil {
		return err
	}
	if err := CheckSealFile(in); err != nil {
		return err
	}
	data := FileSealedData{Bytes: *in.Bytes, Label: in.Label, MediaType: in.MediaType, SHA256: in.SHA256}
	var env *deed.Envelope
	err = h.l.db.OrgTx(r.Context(), o, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		env, err = h.l.RecordAdmin(ctx, tx, AdminEvent{Action: AdminFileSealed, Actor: actor,
			Target: &deed.Ref{Type: "org", ID: o.String()}, Reason: in.Label, RequestID: httpx.RequestID(ctx),
			Data: data})
		return err
	})
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusCreated, SealedFile{Object: "sealed_file", SHA256: data.SHA256, Bytes: data.Bytes,
		MediaType: data.MediaType, Label: data.Label, Deed: env})
}
