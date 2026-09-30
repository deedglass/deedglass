package migrate

import (
	"errors"
	"testing"
	"time"
)

func TestCompareAndProblem(t *testing.T) {
	ms := []Migration{{Version: 1, Name: "a", Checksum: "sha256:1"}, {Version: 2, Name: "b", Checksum: "sha256:2"}, {Version: 3, Name: "c", Checksum: "sha256:3"}}
	now := time.Now()
	rows := compare(ms, []appliedRow{{1, "a", "sha256:1", now}, {2, "b", "sha256:X", now}})
	want := []string{StateApplied, StateChanged, StatePending}
	for i, r := range rows {
		if r.State != want[i] {
			t.Errorf("row %d: %s, want %s", i, r.State, want[i])
		}
	}
	var ce *ChecksumError
	if err := problem(rows, ms); !errors.As(err, &ce) || ce.Version != 2 || ce.Applied != "sha256:X" || ce.File != "sha256:2" || !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("%v", err)
	}
	rows = compare(ms, []appliedRow{{1, "a", "sha256:1", now}, {7, "g", "sha256:7", now}, {5, "e", "sha256:5", now}})
	if len(rows) != 5 || rows[3].State != StateUnknown || rows[4].State != StateUnknown {
		t.Fatalf("%+v", rows)
	}
	var ae *AheadError
	if err := problem(rows, ms); !errors.As(err, &ae) || ae.Version != 7 || ae.Known != 3 || !errors.Is(err, ErrDatabaseAhead) {
		t.Fatalf("%v", err)
	}
	if err := problem(compare(ms, nil), ms); err != nil {
		t.Fatal(err)
	}
}

func TestErrorMessages(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
		is   error
	}{
		{&ChecksumError{3, "policy", "sha256:a", "sha256:b"}, "migration 0003 policy changed after it was applied (applied sha256:a, binary sha256:b)", ErrChecksumMismatch},
		{&AheadError{12, 9}, "the database is at migration 0012, and this build knows up to 0009", ErrDatabaseAhead},
		{&ServerTooOldError{"15.8"}, "Deedglass needs PostgreSQL 16 or newer. This server is 15.8.", ErrServerTooOld},
		{&NotControlPlaneError{"deedglass.ledger"}, "this database has deedglass.ledger, so it's a watched customer database", ErrNotControlPlane},
	} {
		if c.err.Error() != c.want || !errors.Is(c.err, c.is) {
			t.Errorf("%q", c.err.Error())
		}
	}
}
