package httpx

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestIdempotency(t *testing.T) {
	t.Run("first request runs once and is stored; same key and body replays", func(t *testing.T) {
		h := newHarness(t, harnessOpts{keys: fakeKeys{}})
		a := h.post(t, "/v1/_test/echo", `{"a":1}`, bearer(goodKey), idem("K1"))
		b := h.post(t, "/v1/_test/echo", `{"a":1}`, bearer(goodKey), idem("K1"))
		if a.status != 201 || b.status != 201 || string(a.body) != string(b.body) {
			t.Fatalf("a=%d %s\nb=%d %s", a.status, a.body, b.status, b.body)
		}
		if h.runs.Load() != 1 {
			t.Fatalf("handler ran %d times", h.runs.Load())
		}
		if a.header.Get(HeaderReplayed) != "" || b.header.Get(HeaderReplayed) != "true" {
			t.Fatalf("replayed headers: %q %q", a.header.Get(HeaderReplayed), b.header.Get(HeaderReplayed))
		}
		if a.header.Get("Deedglass-Request-Id") == b.header.Get("Deedglass-Request-Id") {
			t.Fatal("a replay must carry its own request id")
		}
		if b.header.Get("Content-Type") != ContentTypeJSON {
			t.Fatalf("replay content type %q", b.header.Get("Content-Type"))
		}
	})
	t.Run("same key, different body is 422", func(t *testing.T) {
		h := newHarness(t, harnessOpts{keys: fakeKeys{}})
		h.post(t, "/v1/_test/echo", `{"a":1}`, bearer(goodKey), idem("K"))
		r := h.post(t, "/v1/_test/echo", `{"a":2}`, bearer(goodKey), idem("K"))
		if r.status != 422 || r.env(t).Code != CodeIdempotencyConflict {
			t.Fatalf("got %d %s", r.status, r.body)
		}
	})
	t.Run("10 concurrent requests run the handler once", func(t *testing.T) {
		h := newHarness(t, harnessOpts{keys: fakeKeys{}})
		h.release = make(chan struct{})
		var wg sync.WaitGroup
		results := make([]resp, 10)
		// The first request blocks inside the handler until the other nine have answered.
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[0] = h.post(t, "/v1/_test/echo", `{"a":1,"block":true}`, bearer(goodKey), idem("C"))
		}()
		for h.runs.Load() == 0 {
			time.Sleep(time.Millisecond)
		}
		var inner sync.WaitGroup
		for i := 1; i < 10; i++ {
			inner.Add(1)
			go func() {
				defer inner.Done()
				results[i] = h.post(t, "/v1/_test/echo", `{"a":1,"block":true}`, bearer(goodKey), idem("C"))
			}()
		}
		inner.Wait()
		close(h.release)
		wg.Wait()
		if h.runs.Load() != 1 {
			t.Fatalf("handler ran %d times", h.runs.Load())
		}
		for i, r := range results[1:] {
			if r.status != 409 || r.env(t).Code != CodeIdempotencyKeyInUse || r.header.Get("Retry-After") != "1" {
				t.Fatalf("request %d: %d %s", i+1, r.status, r.body)
			}
		}
		if results[0].status != 201 {
			t.Fatalf("first: %d", results[0].status)
		}
		after := h.post(t, "/v1/_test/echo", `{"a":1,"block":true}`, bearer(goodKey), idem("C"))
		if after.header.Get(HeaderReplayed) != "true" {
			t.Fatal("after completion the key must replay")
		}
	})
	t.Run("a 500 is stored and replayed", func(t *testing.T) {
		h := newHarness(t, harnessOpts{keys: fakeKeys{}})
		a := h.post(t, "/v1/_test/echo", `{"fail":500}`, bearer(goodKey), idem("E"))
		b := h.post(t, "/v1/_test/echo", `{"fail":500}`, bearer(goodKey), idem("E"))
		if a.status != 500 || b.status != 500 || b.header.Get(HeaderReplayed) != "true" || h.runs.Load() != 1 {
			t.Fatalf("a=%d b=%d replayed=%q runs=%d", a.status, b.status, b.header.Get(HeaderReplayed), h.runs.Load())
		}
		if a.env(t).RequestID != b.env(t).RequestID {
			t.Fatal("the replayed body is the stored one, with the original request id")
		}
	})
	t.Run("a 4xx is stored too", func(t *testing.T) {
		h := newHarness(t, harnessOpts{keys: fakeKeys{}})
		h.post(t, "/v1/_test/echo", `{"fail":409}`, bearer(goodKey), idem("F"))
		b := h.post(t, "/v1/_test/echo", `{"fail":409}`, bearer(goodKey), idem("F"))
		if b.status != 409 || b.header.Get(HeaderReplayed) != "true" || h.runs.Load() != 1 {
			t.Fatalf("b=%d runs=%d", b.status, h.runs.Load())
		}
	})
	t.Run("a 429 is not stored", func(t *testing.T) {
		h := newHarness(t, harnessOpts{keys: fakeKeys{}})
		a := h.post(t, "/v1/_test/echo", `{"fail":429}`, bearer(goodKey), idem("R"))
		b := h.post(t, "/v1/_test/echo", `{"fail":429}`, bearer(goodKey), idem("R"))
		if a.status != 429 || b.status != 429 || b.header.Get(HeaderReplayed) != "" || h.runs.Load() != 2 || a.header.Get("Retry-After") != "3" {
			t.Fatalf("a=%d b=%d runs=%d", a.status, b.status, h.runs.Load())
		}
	})
	t.Run("a panic is stored as the same 500", func(t *testing.T) {
		h := newHarness(t, harnessOpts{keys: fakeKeys{}})
		a := h.post(t, "/v1/_test/panic", `{}`, bearer(goodKey), idem("P"))
		b := h.post(t, "/v1/_test/panic", `{}`, bearer(goodKey), idem("P"))
		if a.status != 500 || b.status != 500 || b.header.Get(HeaderReplayed) != "true" || h.runs.Load() != 1 {
			t.Fatalf("a=%d b=%d replayed=%q runs=%d", a.status, b.status, b.header.Get(HeaderReplayed), h.runs.Load())
		}
		if string(a.body) != string(b.body) {
			t.Fatalf("stored panic body differs:\n%s\n%s", a.body, b.body)
		}
	})
	t.Run("Sensitive: the secret is sent once and never stored", func(t *testing.T) {
		h := newHarness(t, harnessOpts{keys: fakeKeys{}})
		a := h.post(t, "/v1/_test/secret", `{}`, bearer(goodKey), idem("S"))
		b := h.post(t, "/v1/_test/secret", `{}`, bearer(goodKey), idem("S"))
		if !strings.Contains(string(a.body), canarySecret) {
			t.Fatalf("first response lacks the secret: %s", a.body)
		}
		if strings.Contains(string(b.body), canarySecret) || !strings.Contains(string(b.body), `"secret": null`) ||
			!strings.Contains(string(b.body), `"id": "`+testKey.String()+`"`) || b.header.Get(HeaderReplayed) != "true" {
			t.Fatalf("replay: %s", b.body)
		}
		stored, ok := h.store.Stored(IdemScope{OrgID: testOrg, Principal: testAgent.String(), Key: "S"})
		if !ok || strings.Contains(string(stored), canarySecret) || strings.Contains(string(stored), "canary") {
			t.Fatalf("stored: %s", stored)
		}
		if h.runs.Load() != 1 {
			t.Fatalf("runs %d", h.runs.Load())
		}
	})
	t.Run("Sensitive route requires a key", func(t *testing.T) {
		h := newHarness(t, harnessOpts{keys: fakeKeys{}})
		r := h.post(t, "/v1/_test/secret", `{}`, bearer(goodKey))
		if r.status != 400 || r.env(t).Code != CodeIdempotencyKeyInvalid || !strings.Contains(r.env(t).Message, "needs an Idempotency-Key") {
			t.Fatalf("got %d %s", r.status, r.body)
		}
	})
	t.Run("an expired lock is taken over", func(t *testing.T) {
		h := newHarness(t, harnessOpts{keys: fakeKeys{}})
		scope := IdemScope{OrgID: testOrg, Principal: testAgent.String(), Key: "L", Method: "POST", Route: "/v1/_test/echo"}
		fp := fingerprint("POST", "/v1/_test/echo", "/v1/_test/echo", []byte(`{"a":1}`))
		if out, _, _ := h.store.Begin(context.Background(), scope, fp, 35*time.Second); out != Started {
			t.Fatal("setup")
		}
		r := h.post(t, "/v1/_test/echo", `{"a":1}`, bearer(goodKey), idem("L"))
		if r.status != 409 {
			t.Fatalf("while locked: %d", r.status)
		}
		h.advance(36 * time.Second)
		r = h.post(t, "/v1/_test/echo", `{"a":1}`, bearer(goodKey), idem("L"))
		if r.status != 201 || h.runs.Load() != 1 {
			t.Fatalf("after the lock: %d runs=%d", r.status, h.runs.Load())
		}
	})
	t.Run("a 25 hour old key is new again", func(t *testing.T) {
		h := newHarness(t, harnessOpts{keys: fakeKeys{}})
		h.post(t, "/v1/_test/echo", `{"a":1}`, bearer(goodKey), idem("OLD"))
		h.advance(25 * time.Hour)
		r := h.post(t, "/v1/_test/echo", `{"a":2}`, bearer(goodKey), idem("OLD"))
		if r.status != 201 || r.header.Get(HeaderReplayed) != "" || h.runs.Load() != 2 {
			t.Fatalf("got %d replayed=%q runs=%d", r.status, r.header.Get(HeaderReplayed), h.runs.Load())
		}
		if n := h.store.Prune(); n != 0 {
			t.Fatalf("prune removed %d live keys", n)
		}
		h.advance(25 * time.Hour)
		if n := h.store.Prune(); n != 1 {
			t.Fatalf("prune removed %d, want 1", n)
		}
	})
	t.Run("another principal with the same key is independent", func(t *testing.T) {
		h := newHarness(t, harnessOpts{keys: fakeKeys{}})
		a := h.post(t, "/v1/_test/echo", `{"a":1}`, bearer(goodKey), idem("SAME"))
		b := h.post(t, "/v1/_test/echo", `{"a":1}`, bearer(otherKey), idem("SAME"))
		if a.status != 201 || b.status != 201 || b.header.Get(HeaderReplayed) != "" || h.runs.Load() != 2 {
			t.Fatalf("a=%d b=%d runs=%d", a.status, b.status, h.runs.Load())
		}
	})
	t.Run("key validation", func(t *testing.T) {
		h := newHarness(t, harnessOpts{keys: fakeKeys{}})
		for _, k := range []string{strings.Repeat("k", 256), "tab\there", "ünïcode"} {
			r := h.post(t, "/v1/_test/echo", `{"a":1}`, bearer(goodKey), idem(k))
			if r.status != 400 || r.env(t).Code != CodeIdempotencyKeyInvalid || *r.env(t).Param != "Idempotency-Key" {
				t.Fatalf("key %q: %d %s", k, r.status, r.body)
			}
		}
		r := h.post(t, "/v1/_test/echo", `{"a":1}`, bearer(goodKey), idem(strings.Repeat("k", 255)))
		if r.status != 201 {
			t.Fatalf("255 characters: %d", r.status)
		}
		if h.runs.Load() != 1 {
			t.Fatalf("runs %d", h.runs.Load())
		}
	})
	t.Run("errors before the handler are not stored", func(t *testing.T) {
		h := newHarness(t, harnessOpts{keys: fakeKeys{}})
		a := h.post(t, "/v1/_test/echo", `{"a":1}`, bearer("dg_test_wrong"), idem("A"))
		b := h.post(t, "/v1/_test/echo", `{"a":1}`, bearer(goodKey), idem("A"))
		if a.status != 401 || b.status != 201 || b.header.Get(HeaderReplayed) != "" {
			t.Fatalf("a=%d b=%d", a.status, b.status)
		}
	})
	t.Run("a Public route ignores the key", func(t *testing.T) {
		h := newHarness(t, harnessOpts{keys: fakeKeys{}})
		h.post(t, "/v1/_test/public", `{}`, idem("PUB"))
		r := h.post(t, "/v1/_test/public", `{}`, idem("PUB"))
		if r.status != 200 || r.header.Get(HeaderReplayed) != "" || h.runs.Load() != 2 {
			t.Fatalf("got %d runs=%d", r.status, h.runs.Load())
		}
	})
	t.Run("the fingerprint covers method, pattern, path and body", func(t *testing.T) {
		base := fingerprint("POST", "/v1/x/{id}", "/v1/x/1", []byte("{}"))
		for _, other := range []string{
			fingerprint("PUT", "/v1/x/{id}", "/v1/x/1", []byte("{}")),
			fingerprint("POST", "/v1/y/{id}", "/v1/x/1", []byte("{}")),
			fingerprint("POST", "/v1/x/{id}", "/v1/x/2", []byte("{}")),
			fingerprint("POST", "/v1/x/{id}", "/v1/x/1", []byte("{ }")),
		} {
			if other == base {
				t.Fatal("fingerprint collision")
			}
		}
		if !strings.HasPrefix(base, "sha256:") || len(base) != 71 {
			t.Fatalf("form %q", base)
		}
	})
}

func TestRedactJSON(t *testing.T) {
	in := []byte("{\n  \"object\": \"api_key\",\n  \"secret\": \"s3cr3t\",\n  \"nested\": {\"secret\": \"keep\"}\n}\n")
	got := string(redactJSON(in, []string{"secret"}))
	if strings.Contains(got, "s3cr3t") || !strings.Contains(got, `"secret": null`) || !strings.Contains(got, `"keep"`) {
		t.Fatalf("got %s", got)
	}
	if strings.Index(got, "object") > strings.Index(got, "secret") {
		t.Fatal("member order changed")
	}
	for _, bad := range []string{`["s3cr3t"]`, `"s3cr3t"`, `{"secret":`, `nope`} {
		if out := string(redactJSON([]byte(bad), []string{"secret"})); strings.Contains(out, "s3cr3t") {
			t.Fatalf("%s → %s", bad, out)
		}
	}
}
