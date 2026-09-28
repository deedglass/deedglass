// Package verify turns an assembled transaction into a verdict (spec §10). It
// classifies each frame (C0 ignore; C1 UNBOUND_MUTATION; C2 FORGED_MARKER; C3
// FORGED_MARKER_NONTX; C4 MULTIPLE_OPERATIONS / MARKER_COUNT; C5 checked),
// runs all fourteen ordered checks for C5 and collects every reason code, and
// compares the expected delta computed by the bundle rule from the signed
// intent and the decoded old images with the observed delta, writing a diff on
// mismatch. Precedence: any contradiction gives INVALID, otherwise any gap
// gives INCONCLUSIVE, otherwise VALID. No code path may turn an evidence gap
// into INVALID or anything but a clean pass into VALID; the precedence
// function is tested over every combination of reasons. marker_lsn is
// evidence only and is never compared with commit_lsn (plan §3.0 #1).
package verify
