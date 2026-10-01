package deed

import (
	"bytes"
	"encoding/json"
)

// MessageCatalog is testdata/deeds/messages.json: every code and every message template, so a
// verifier in another language shows the same copy (the browser verifier imports a generated copy).
type MessageCatalog struct {
	Object      string                     `json:"object"` // "deed_message_catalog"
	GeneratedBy string                     `json:"generated_by"`
	Codes       []Code                     `json:"codes"`
	Messages    map[string]MessageTemplate `json:"messages"`
}

// MessageCatalogJSON renders the catalog as indented JSON with a final newline. The vector
// generator and `go run ./internal/deed/cmd/gencopy` both write exactly these bytes.
func MessageCatalogJSON() []byte {
	c := MessageCatalog{Object: "deed_message_catalog", GeneratedBy: "go run ./internal/deed/cmd/gencopy (from internal/deed/messages.go)",
		Codes: Codes, Messages: Messages}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(c); err != nil {
		panic(err) // static data
	}
	return b.Bytes()
}
