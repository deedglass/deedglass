package rest_test

import (
	"context"
	"slices"
	"testing"

	"github.com/deedglass/deedglass/internal/connector"
)

// HC-P09: a REST tool's path args are its identifiers; body, query and header args are not.
func TestPHIKeep(t *testing.T) {
	r, _ := newRig(t, (&orders{status: map[string]string{}}).handle, "")
	ex, spec, _, err := r.reg.Resolve(context.Background(), org, "orders.update_status")
	if err != nil {
		t.Fatal(err)
	}
	k := ex.(connector.PHIKeeping).PHIKeep(spec)
	if !slices.Equal(k.Args, []string{"id"}) || !slices.Contains(k.Result, "headers.etag") || slices.Contains(k.Result, "body") {
		t.Fatalf("%+v", k)
	}
	ex, spec, _, _ = r.reg.Resolve(context.Background(), org, "orders.search")
	if k := ex.(connector.PHIKeeping).PHIKeep(spec); len(k.Args) != 0 {
		t.Fatalf("search has no path args: %+v", k)
	}
}
