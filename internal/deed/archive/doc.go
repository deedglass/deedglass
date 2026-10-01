// Package archive writes and reads export bundle v1 (docs/BUNDLE_FORMAT.md, contract §7): a
// deterministic gzip-compressed tar holding exactly README.txt, keys.json, deeds.jsonl,
// manifest.json and an optional manifest.seal.json, at the root, in that order.
//
// Keys come before deeds so a reader can verify while streaming; the manifest comes after the deeds
// because it carries their SHA-256, so a writer can stream too.
//
// Invariants (each has a test):
//   - The same inputs always produce the same bytes: USTAR headers (PAX only past 8 GiB), mode
//     0644, uid/gid 0, empty user and group names, mtime = created_at; gzip with no name, mtime 0
//     and OS 255.
//   - The manifest is JCS; its seal signs "deedglass/bundle/v1\n" + sha256(manifest.json bytes).
//   - The reader never writes to disk and refuses, before using a byte, any entry that is not one of
//     the five names, a duplicate, a path with "/" or "..", a non-regular entry, a sixth entry, a
//     file over its limit, or a total that expands past the sum of the limits (a gzip bomb).
//   - Every refusal is a *deed.VerifyError with code bundle_invalid and a one-sentence detail.
//
// API (S05; S06's `deedglass deeds export` and `GET /v1/deeds/export` call the Writer):
//
//	func NewWriter(w io.Writer, h Header, keysJSON []byte, deedsBytes, deedsLines int64) (*Writer, error)
//	func (w *Writer) WriteDeed(canonicalEnvelope []byte) error
//	func (w *Writer) Close(s deed.Signer) (*Manifest, error)
//	func Open(r io.Reader, lim Limits) (*Reader, error)
//	func (r *Reader) Keys() []byte
//	func (r *Reader) Deeds() io.Reader
//	func (r *Reader) Finish() (*Manifest, *deed.BundleSeal, error)
//	func (m *Manifest) Check(obs Observed) error
//	func FileName(org string, firstSeq, lastSeq int64) string
package archive
