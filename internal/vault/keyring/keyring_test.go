package keyring

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	k := func(b byte, n int) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, n)) }
	raw := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	ok := []string{k(1, 32), k(1, 32) + "," + k(2, 32), " " + k(1, 32) + " , " + raw, strings.Join([]string{k(1, 32), k(2, 32), k(3, 32), k(4, 32)}, ",")}
	for _, s := range ok {
		ks, err := Parse(s)
		if err != nil {
			t.Errorf("%.12s…: %v", s, err)
			continue
		}
		for _, x := range ks {
			if !strings.HasPrefix(x.ID, "kek:") || len(x.ID) != 16 || len(x.Bytes) != KeyLen {
				t.Errorf("key %s", x.ID)
			}
		}
	}
	bad := map[string]string{
		k(1, 31):                  "a first key of 31 bytes",
		k(1, 32) + ",":            "an empty second key",
		k(1, 32) + ",!!":          "a second key that isn't base64",
		k(1, 32) + "," + k(1, 32): "the same key twice (the first and the second, kek:",
		strings.Repeat(k(1, 32)+",", 4) + k(5, 32): "holds 5 keys",
	}
	for s, want := range bad {
		_, err := Parse(s)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%.12s…: %v, want %q", s, err, want)
		}
		if err != nil && strings.Contains(err.Error(), k(1, 32)) {
			t.Error("a problem prints a key")
		}
	}
	// The id is stable and depends only on the key.
	if ID(bytes.Repeat([]byte{1}, 32)) != ID(bytes.Repeat([]byte{1}, 32)) || ID([]byte("a")) == ID([]byte("b")) {
		t.Error("ids")
	}
	ks, _ := Parse(k(1, 32) + "," + k(2, 32))
	if ids := IDs(ks); len(ids) != 2 || ids[0] == ids[1] {
		t.Error(ids)
	}
}
