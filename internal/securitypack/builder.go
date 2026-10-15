package securitypack

import (
	"context"
	"encoding/json"
	"io"
	"time"
)

// FileData is what a sealed file's deed seals (ledger.FileSealedData).
type FileData struct {
	SHA256    string
	Bytes     int64
	MediaType string
	Label     string
}

// ExportFunc writes the org's records issued in [from, to) as an export bundle v1 and returns how
// many it wrote. ErrNoRecords when there are none.
type ExportFunc func(ctx context.Context, from, to time.Time, w io.Writer) (int64, error)

// SealFunc seals one file as the admin deed file.sealed on behalf of the person building the pack
// and returns the deed envelope's canonical bytes.
type SealFunc func(ctx context.Context, d FileData) (json.RawMessage, error)

// KeysFunc returns the server's public seal keys (keys.json, a seal_key_set).
type KeysFunc func(ctx context.Context) (json.RawMessage, error)

// BuildRequest is one pack build. Facts are the org's, collected by the caller.
type BuildRequest struct {
	Facts   *Facts
	Aliases bool
	Records bool
	Export  ExportFunc
	Seal    SealFunc
	Keys    KeysFunc
	// TempDir is where the records bundle is staged while it is hashed ("" for the system's).
	TempDir string
}

// BuildResult says what the folder holds.
type BuildResult struct {
	Folder  string  `json:"folder"`
	Answers int     `json:"answers"`
	Records int64   `json:"records"`
	Sealed  int     `json:"sealed"`
	Summary Summary `json:"summary"`
	Aliases bool    `json:"aliases"`
}

// Builder writes a pack folder as a tar stream (ee/securitypack, Pro). Serve passes one, or nil
// when the licence doesn't grant security_pack.
type Builder interface {
	Build(ctx context.Context, req BuildRequest, w io.Writer) (*BuildResult, error)
}

// ErrNoRecords is ExportFunc's answer when the period holds no records.
var ErrNoRecords = errNoRecords{}

type errNoRecords struct{}

func (errNoRecords) Error() string { return "securitypack: no records in the period" }
