package jsonrpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
)

// TestFraming is S14 §6's framing table.
func TestFraming(t *testing.T) {
	big := `{"jsonrpc":"2.0","id":1,"method":"x","params":{"s":"` + strings.Repeat("a", DefaultMaxLine) + `"}}`
	deep := `{"jsonrpc":"2.0","id":1,"method":"x","params":` + strings.Repeat("[", 128) + strings.Repeat("]", 128) + `}`
	ok := `{"jsonrpc":"2.0","id":1,"method":"x","params":` + strings.Repeat("[", 127) + strings.Repeat("]", 127) + `}`
	cases := []struct {
		name string
		line string
		kind Kind
		err  error
		code int
	}{
		{"numeric id", `{"jsonrpc":"2.0","id":7,"method":"tools/list"}`, Request, nil, 0},
		{"string id", `{"jsonrpc":"2.0","id":"a-1","method":"tools/list"}`, Request, nil, 0},
		{"notification", `{"jsonrpc":"2.0","method":"notifications/initialized"}`, Notification, nil, 0},
		{"response", `{"jsonrpc":"2.0","id":3,"result":{}}`, Response, nil, 0},
		{"error response with null id", `{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"x"}}`, Response, nil, 0},
		{"too large", big, Invalid, ErrTooLarge, CodeInvalidRequest},
		{"truncated", `{"jsonrpc":"2.0",`, Invalid, ErrParse, CodeParseError},
		{"batch", `[{"jsonrpc":"2.0","id":1,"method":"x"}]`, Invalid, ErrBatch, CodeInvalidRequest},
		{"depth 129", deep, Invalid, ErrTooDeep, CodeInvalidRequest},
		{"depth 128", ok, Request, nil, 0},
		{"unknown member", `{"jsonrpc":"2.0","id":1,"method":"x","x-ext":1}`, Request, nil, 0},
		{"not utf-8", "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"\xff\"}", Invalid, ErrNotUTF8, CodeParseError},
		{"wrong version", `{"jsonrpc":"1.0","id":1,"method":"x"}`, Invalid, ErrShape, CodeInvalidRequest},
		{"no method no result", `{"jsonrpc":"2.0","id":1}`, Invalid, ErrShape, CodeInvalidRequest},
		{"duplicate member", `{"jsonrpc":"2.0","id":1,"method":"tools/list","method":"tools/call"}`, Invalid, ErrShape, CodeInvalidRequest},
		{"a string", `"hello"`, Invalid, ErrShape, CodeInvalidRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := NewReader(strings.NewReader(c.line + "\n" + `{"jsonrpc":"2.0","method":"next"}` + "\n"))
			e, line, err := r.Next()
			if !errors.Is(err, c.err) {
				t.Fatalf("err %v, want %v", err, c.err)
			}
			if err != nil {
				if code, _ := Reply(err); code != c.code {
					t.Fatalf("code %d, want %d", code, c.code)
				}
			} else {
				if e.Kind() != c.kind {
					t.Fatalf("kind %v", e.Kind())
				}
				if string(line) != c.line {
					t.Fatal("line changed")
				}
			}
			// The reader resyncs at the next line.
			e, _, err = r.Next()
			if err != nil || e.Method() != "next" {
				t.Fatalf("resync: %v %v", e, err)
			}
			if _, _, err := r.Next(); err != io.EOF {
				t.Fatalf("eof: %v", err)
			}
		})
	}
}

func TestIDAndMembersPreserved(t *testing.T) {
	line := `{"jsonrpc":"2.0","id":"a-1","method":"tools/call","params":{"name":"x","arguments":{"b":1.50,"a":"<&>"}},"x-ext":{"k":[1, 2]}}`
	e, err := Parse([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	if string(e.ID()) != `"a-1"` {
		t.Fatalf("id %s", e.ID())
	}
	out := e.Marshal()
	if !bytes.Contains(out, []byte(`"params":{"name":"x","arguments":{"b":1.50,"a":"<&>"}}`)) || !bytes.Contains(out, []byte(`"x-ext":{"k":[1, 2]}`)) {
		t.Fatalf("members changed: %s", out)
	}
	e["id"] = Int(42)
	if !bytes.HasPrefix(e.Marshal(), []byte(`{"jsonrpc":"2.0","id":42,"method":"tools/call"`)) {
		t.Fatalf("%s", e.Marshal())
	}
}

func TestEra(t *testing.T) {
	e, _ := Parse([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`))
	if e.ModernVersion() != "2026-07-28" {
		t.Fatal(e.ModernVersion())
	}
	e, _ = Parse([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	if e.ModernVersion() != "" {
		t.Fatal("legacy")
	}
}

func TestFinalLineWithoutNewline(t *testing.T) {
	r := NewReader(strings.NewReader(`{"jsonrpc":"2.0","method":"a"}`))
	e, _, err := r.Next()
	if err != nil || e.Method() != "a" {
		t.Fatal(err)
	}
	if _, _, err := r.Next(); err != io.EOF {
		t.Fatal(err)
	}
}

func TestTooLargeKeepsHeadAndTail(t *testing.T) {
	r := NewReader(strings.NewReader(`{"jsonrpc":"2.0","id":57,"result":{"s":"` + strings.Repeat("a", DefaultMaxLine) + `"}}` + "\n"))
	if _, _, err := r.Next(); !errors.Is(err, ErrTooLarge) {
		t.Fatal(err)
	}
	if !bytes.Contains(r.Head, []byte(`"id":57`)) || !bytes.HasSuffix(bytes.TrimSpace(r.Tail), []byte(`"}}`)) {
		t.Fatal("head and tail")
	}
}

func TestWriterOneLinePerMessage(t *testing.T) {
	var buf safeBuf
	w := NewWriter(&buf)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = w.SendEnvelope(NewNotification("n", Value(map[string]int{"i": i})))
		}(i)
	}
	wg.Wait()
	if err := w.Send([]byte("a\nb")); err == nil {
		t.Fatal("newline accepted")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 50 {
		t.Fatalf("%d lines", len(lines))
	}
	for _, l := range lines {
		if _, err := Parse([]byte(l)); err != nil {
			t.Fatalf("%q: %v", l, err)
		}
	}
	if w.Send([]byte("{}")) != ErrClosed {
		t.Fatal("send after close")
	}
}

func TestNewError(t *testing.T) {
	e := NewError(nil, CodeInvalidRequest, "Batches are not supported.")
	var v struct {
		ID    json.RawMessage
		Error ErrorObject
	}
	if json.Unmarshal(e.Marshal(), &v) != nil || string(v.ID) != "null" || v.Error.Code != -32600 {
		t.Fatalf("%s", e.Marshal())
	}
}

// FuzzReader: arbitrary bytes never panic, and every message the reader returns parses again on
// its own (never a partial message).
func FuzzReader(f *testing.F) {
	f.Add([]byte(`{"jsonrpc":"2.0","id":1,"method":"x"}` + "\n"))
	f.Add([]byte("{\"jsonrpc\":\"2.0\",\n\"id\":1}\n[1]\n\xff\n"))
	f.Add([]byte(`{"jsonrpc":"2.0","id":1,"result":{"a":[[[[[]]]]]}}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		r := NewReader(bytes.NewReader(b))
		r.MaxLine = 256
		for i := 0; i < 1000; i++ {
			e, line, err := r.Next()
			if err == io.EOF {
				return
			}
			if err != nil {
				if errors.Is(err, ErrTooLarge) || errors.Is(err, ErrParse) || errors.Is(err, ErrBatch) || errors.Is(err, ErrTooDeep) ||
					errors.Is(err, ErrNotUTF8) || errors.Is(err, ErrShape) {
					continue
				}
				return
			}
			if bytes.IndexByte(line, '\n') >= 0 {
				t.Fatal("newline inside a message")
			}
			if _, err := Parse(e.Marshal()); err != nil {
				t.Fatalf("re-marshal: %v", err)
			}
		}
	})
}

type safeBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestHelpers(t *testing.T) {
	for k, want := range map[Kind]string{Invalid: "invalid", Request: "request", Notification: "notification", Response: "response"} {
		if k.String() != want {
			t.Fatal(k)
		}
	}
	e := NewRequest(3, "tools/list", MarshalObject(map[string]json.RawMessage{"_meta": Value(map[string]string{MetaProtocolVersion: "2026-07-28"})}))
	if e.Kind() != Request || e.Method() != "tools/list" || e.ModernVersion() != "2026-07-28" || string(e.ID()) != "3" {
		t.Fatalf("%s", e.Marshal())
	}
	n := NewNotification("notifications/initialized", nil)
	if n.Kind() != Notification || n.ID() != nil || n.Meta() != nil {
		t.Fatal("notification")
	}
	r := NewResponse(nil, Null)
	if string(r.ID()) != "null" || r.Kind() != Response {
		t.Fatal("response")
	}
	if string(String("<a>")) != `"<a>"` || string(Value(map[string]int{"b": 1, "a": 2})) != `{"a":2,"b":1}` {
		t.Fatal("encoding")
	}
	for _, err := range []error{ErrTooLarge, ErrBatch, ErrTooDeep, ErrShape, ErrParse, ErrNotUTF8} {
		if code, msg := Reply(err); code == 0 || !strings.HasSuffix(msg, ".") {
			t.Fatal(err)
		}
	}
	w := NewWriter(errWriter{})
	_ = w.Send([]byte(`{}`))
	if err := w.Close(); err == nil || w.Err() == nil {
		t.Fatal("write error")
	}
}

type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
