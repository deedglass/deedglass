// Package healthcare carries the healthcare pages the binary prints: the hospital IT review pack
// (`deedglass health pilot --print-review-pack`).
package healthcare

import _ "embed"

// ITReviewPack is IT_REVIEW_PACK.md.
//
//go:embed IT_REVIEW_PACK.md
var ITReviewPack []byte
