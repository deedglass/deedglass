package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/cli/ui"
	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/deed/stamp"
	"github.com/deedglass/deedglass/internal/deed/stamp/fetch"
	"github.com/deedglass/deedglass/internal/ledger"
)

// Head timestamps (TY-03, ADR-112): `deedglass deeds timestamp` and the stamper exports use. Off
// unless timestamps.tsa_urls or timestamps.scitt_url is set; with neither, nothing here runs and
// no request goes out. Every request leaves through the egress door, like a connector's.

// stampTransport replaces the egress transport in tests (a loopback fake TSA); nil in production.
var stampTransport http.RoundTripper

// stamperFor returns the Ledger's stamper for cfg, or nil when timestamps are off. signer seals
// the transparency-service statement; without one, a configured service fails each stamp.
func stamperFor(cfg *config.Config, signer deed.Signer, log *slog.Logger) ledger.Stamper {
	t := cfg.Timestamps
	if !t.Enabled() {
		return nil
	}
	opts := egress.ClientOptions{Timeout: t.Timeout, HeaderTimeout: t.Timeout, MaxResponseBytes: fetch.MaxResponse}
	var client *http.Client
	if stampTransport != nil {
		client = egress.Wrap(stampTransport, opts)
	} else {
		client = egressPolicy(context.Background(), cfg, log).Client(opts)
	}
	issuer := ""
	if cfg.BaseURL != nil {
		issuer = strings.TrimSuffix(cfg.BaseURL.String(), "/")
	}
	return func(ctx context.Context, head stamp.Head) (stamp.Entry, error) {
		c := &fetch.Config{TSAs: t.TSAURLs, MinTSA: t.MinTSA, Service: t.SCITTURL, Signer: signer, Issuer: issuer,
			Timeout: t.Timeout, Client: client}
		if t.SCITTURL != "" {
			k, err := readServiceKey(t.SCITTKey)
			if err != nil {
				return stamp.Entry{}, err
			}
			c.ServiceKey = &k
		}
		return fetch.Stamp(ctx, c, head)
	}
}

// readServiceKey reads timestamps.scitt_key_file: one JWK (EC P-256 or OKP Ed25519).
func readServiceKey(path string) (stamp.JWK, error) {
	var k stamp.JWK
	raw, err := readCapped(path, 64<<10)
	if err != nil {
		return k, fmt.Errorf("can't read timestamps.scitt_key_file %s: %s", clean(path), err.Error())
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	if err := dec.Decode(&k); err != nil {
		return k, fmt.Errorf("timestamps.scitt_key_file %s is not a JWK", clean(path))
	}
	if err := k.Validate(); err != nil {
		return k, fmt.Errorf("timestamps.scitt_key_file %s: %v", clean(path), err)
	}
	return k, nil
}

// --- deeds timestamp ---

type timestampOpts struct {
	out   string
	seq   int64
	every time.Duration
	count int
}

var timestampValues timestampOpts

func deedsTimestampFlags(fs *flag.FlagSet) {
	o := &timestampValues
	*o = timestampOpts{}
	fs.StringVar(&o.out, "out", "timestamps.json", "the timestamps file to add the head to (created if missing)")
	fs.Int64Var(&o.seq, "seq", 0, "timestamp this record instead of the chain's last one")
	fs.DurationVar(&o.every, "every", 0, "keep running and timestamp the head this often when it has moved (for example 15m)")
	fs.IntVar(&o.count, "count", 0, "with --every, stop after this many timestamps (0: until stopped)")
}

func runDeedsTimestamp(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass deeds timestamp takes no arguments (got %q)", args[0]), "Use --out <file>.")
	}
	o := timestampValues
	switch {
	case o.every < 0 || (o.every > 0 && o.every < time.Minute):
		return usageErr(errors.New("--every must be at least 1m"), "For example --every 15m.")
	case o.seq < 0:
		return usageErr(errors.New("--seq must be positive"), "Pass the seq of a record, or leave it out for the last one.")
	case o.every > 0 && o.seq > 0:
		return usageErr(errors.New("--every timestamps the moving head; it can't be used with --seq"), "Leave out --seq.")
	}
	s, err := openLedger(ctx, e, true)
	if err != nil {
		return err
	}
	defer s.Close()
	stamper := stamperFor(s.cfg, s.signer, slog.New(slog.DiscardHandler))
	if stamper == nil {
		return failNext(e, ExitUsage, "timestamps_off", "Timestamps are off: no TSA or transparency service is configured.",
			"Set timestamps.tsa_urls (and optionally timestamps.scitt_url) in deedglass.yaml. docs/TIMESTAMPS.md has examples.")
	}
	var last int64
	for n := 0; ; {
		h, err := s.led.Head(ctx, s.org.ID, o.seq)
		if errors.Is(err, ledger.ErrNotFound) {
			msg := "No records in this org yet."
			if o.seq > 0 {
				msg = fmt.Sprintf("No record %s in this org.", deed.FormatCount(o.seq))
			}
			return failNext(e, ExitFailure, "not_found", msg, "Run it again once a record is sealed.")
		}
		if err != nil {
			return failNext(e, ExitFailure, "database_error", "Can't read the chain head.", "Check the database, then run it again.")
		}
		if h.Seq != last {
			if err := stampOnce(ctx, e, stamper, h, o.out); err != nil {
				if o.every == 0 {
					return err
				}
			} else {
				last = h.Seq
				n++
			}
		}
		if o.every == 0 || (o.count > 0 && n >= o.count) {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(o.every):
		}
	}
}

// stampOnce gets evidence for h and adds it to the file at path (atomically).
func stampOnce(ctx context.Context, e *Env, stamper ledger.Stamper, h stamp.Head, path string) error {
	f := stamp.NewFile(h.Org)
	if raw, err := os.ReadFile(path); err == nil {
		if f, err = stamp.ParseFile(raw, stamp.MaxFileBytes); err != nil {
			return failNext(e, ExitFailure, "timestamp_failed", fmt.Sprintf("%s is not a timestamps file (%s).", clean(path), err.Error()),
				"Choose another file with --out, or move this one aside.")
		}
		if f.Org != h.Org {
			return failNext(e, ExitFailure, "timestamp_failed", fmt.Sprintf("%s holds another org's timestamps.", clean(path)),
				"Keep one file per org: choose another file with --out.")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return failNext(e, ExitFailure, "timestamp_failed", fmt.Sprintf("Can't read %s.", clean(path)), "Check the path and its permissions.")
	}
	entry, err := stamper(ctx, h)
	if err != nil {
		return failNext(e, ExitFailure, "timestamp_failed", "Record "+deed.FormatCount(h.Seq)+" was not timestamped: "+strings.TrimPrefix(err.Error(), "stamp: ")+".",
			"Check the TSA URLs in timestamps.tsa_urls and that this machine can reach them, then run it again.")
	}
	if err := f.Add(entry); err != nil {
		return failNext(e, ExitFailure, "timestamp_failed", strings.TrimPrefix(err.Error(), "stamp: ")+".", "Choose another file with --out.")
	}
	b, err := f.Marshal()
	if err != nil {
		return failNext(e, ExitFailure, "timestamp_failed", "The timestamps file could not be written ("+err.Error()+").", "Run it again.")
	}
	if err := writeFileAtomic(path, b); err != nil {
		return failNext(e, ExitFailure, "write_failed", fmt.Sprintf("Can't write %s.", clean(path)), "Choose another file with --out.")
	}
	if e.Globals.JSON {
		return json.NewEncoder(e.Stdout).Encode(map[string]any{"object": "head_timestamp", "file": path, "head": entry.Head,
			"rfc3161": len(entry.RFC3161), "scitt": len(entry.SCITT)})
	}
	var by []string
	if n := len(entry.RFC3161); n > 0 {
		by = append(by, count(int64(n), "TSA", "TSAs"))
	}
	if n := len(entry.SCITT); n > 0 {
		by = append(by, count(int64(n), "transparency service", "transparency services"))
	}
	fmt.Fprintln(e.Stdout, e.Style.OKLine(fmt.Sprintf("Record %s timestamped by %s %s %s", deed.FormatCount(h.Seq), strings.Join(by, " and "), ui.Sep, clean(path))))
	return nil
}
