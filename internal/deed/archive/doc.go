// Package archive writes and reads export bundle v1: a deterministic tar.gz holding README.txt,
// keys.json, deeds.jsonl, manifest.json and manifest.seal.json, in that order (contract §7).
//
// Invariants:
//   - The same deeds and keys always produce the same bytes (fixed order, times and modes).
//   - The manifest seal input is "deedglass/bundle/v1\n" + manifest hash.
//   - Reading never trusts the archive's own keys.json unless the caller asks it to.
//
// Planned API (finalized by S05):
//
//	func Write(w io.Writer, deeds iter.Seq[[]byte], keys KeySet, seal Sealer) error
//	func Read(r io.Reader) (*Bundle, error)
//
// Filled by: S05
package archive
