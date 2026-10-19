package fhirtest

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// The Postgres store (HC-P05, ADR-108): with UsePostgres the server also writes every version into
// HAPI FHIR JPA 8.x's own tables in a Postgres database, one transaction per write, as HC-P05's run
// of v8.12.0-2 showed: an hfj_res_ver INSERT, then an UPDATE of that row setting request_id (the
// X-Request-ID header, or an id the server makes), the hfj_resource row, and a search-index row.
// hfj_res_ver has HAPI 8's exact columns, so the decoder and the fhir connector's mapping read it as
// they read HAPI. Knobs: PlaceholderRefs (a create that names an unknown Patient commits that Patient
// too, I07), LegacyText (the text goes into a large object as pre-7.0 HAPI stored it, I08),
// Interleave (another writer's version of the same resource commits while the call is open, I05),
// ForgeRequestID (another client's write carrying the call's X-Request-ID commits while the call is
// open, I06).

// hapiSchema is HAPI 8.x's version, resource, tag and token-index tables (the columns the mapping
// checks, with HAPI's types; the decoder sees exactly these).
const hapiSchema = `
CREATE TABLE IF NOT EXISTS hfj_resource (res_id bigint PRIMARY KEY, partition_id integer, res_deleted_at timestamp, res_version varchar(7),
  has_tags boolean NOT NULL DEFAULT false, res_published timestamp NOT NULL, res_updated timestamp NOT NULL, fhir_id varchar(64) NOT NULL,
  sp_has_links boolean, hash_sha256 varchar(64), sp_index_status smallint, res_language varchar(20), partition_date date,
  res_type varchar(40) NOT NULL, res_type_id smallint, search_url_present boolean, res_ver bigint NOT NULL,
  UNIQUE (res_type, fhir_id));
CREATE TABLE IF NOT EXISTS hfj_res_ver (partition_id integer, pid bigint PRIMARY KEY, res_deleted_at timestamp, res_version varchar(7),
  has_tags boolean NOT NULL, res_published timestamp NOT NULL, res_updated timestamp NOT NULL, res_encoding varchar(5) NOT NULL,
  partition_date date, request_id varchar(16), res_text oid, res_id bigint NOT NULL, res_text_vc text, res_type varchar(40) NOT NULL,
  res_type_id smallint, res_ver bigint NOT NULL, source_uri varchar(768));
CREATE TABLE IF NOT EXISTS hfj_tag_def (tag_id bigint PRIMARY KEY, tag_code varchar(200), tag_display varchar(200), tag_system varchar(200),
  tag_type integer NOT NULL, tag_user_selected boolean, tag_version varchar(30));
CREATE TABLE IF NOT EXISTS hfj_history_tag (pid bigint PRIMARY KEY, partition_id integer, partition_date date, tag_id bigint NOT NULL,
  res_ver_pid bigint NOT NULL, res_id bigint NOT NULL, res_type varchar(40) NOT NULL, res_type_id smallint);
CREATE TABLE IF NOT EXISTS hfj_spidx_token (sp_id bigint PRIMARY KEY, partition_id integer, partition_date date, hash_identity bigint,
  sp_missing boolean NOT NULL DEFAULT false, sp_name varchar(100) NOT NULL, res_id bigint NOT NULL, res_type varchar(100) NOT NULL,
  sp_updated timestamp, hash_sys bigint, hash_sys_and_value bigint, hash_value bigint, sp_system varchar(200), sp_value varchar(200));
CREATE SEQUENCE IF NOT EXISTS fhirtest_pid START 9000;
`

// HAPITables are the tables a witness publication holds (schema public).
var HAPITables = []string{"public.hfj_resource", "public.hfj_res_ver", "public.hfj_history_tag", "public.hfj_tag_def", "public.hfj_spidx_token"}

// UsePostgres makes the server also store every version in HAPI's tables in the database at dsn
// (creating them). Call it before the first request.
func (s *Server) UsePostgres(ctx context.Context, dsn string) error {
	c, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	if _, err := c.Exec(ctx, hapiSchema); err != nil {
		_ = c.Close(ctx)
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.db = c
	s.resIDs = map[string]int64{}
	return nil
}

// pending is one version to write in the current transaction.
type pending struct {
	typ, id, vid string
	res          map[string]any // nil: a delete
	reqID        string
}

func newRequestID() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

// persist writes versions in one transaction, as HAPI does; s.mu is held.
func (s *Server) persist(vs ...pending) error {
	if s.db == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	now := time.Now().UTC().Format("2006-01-02 15:04:05.000")
	for _, v := range vs {
		key := v.typ + "/" + v.id
		resID, known := s.resIDs[key]
		if !known {
			if err := tx.QueryRow(ctx, `SELECT nextval('fhirtest_pid')`).Scan(&resID); err != nil {
				return err
			}
			s.resIDs[key] = resID
		}
		var pid int64
		if err := tx.QueryRow(ctx, `SELECT nextval('fhirtest_pid')`).Scan(&pid); err != nil {
			return err
		}
		enc, text, deleted := "JSON", any(nil), any(nil)
		var lo any
		if v.res == nil {
			enc, deleted = "DEL", now
		} else {
			body := map[string]any{}
			for k, x := range v.res {
				if k != "id" && k != "meta" {
					body[k] = x
				}
			}
			b, _ := json.Marshal(body)
			if s.LegacyText {
				enc = "JSONC"
				if err := tx.QueryRow(ctx, `SELECT lo_from_bytea(0, $1)`, b).Scan(&lo); err != nil {
					return err
				}
			} else {
				text = string(b)
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO hfj_res_ver (pid, res_deleted_at, res_version, has_tags, res_published, res_updated, res_encoding,
		    res_text, res_id, res_text_vc, res_type, res_type_id, res_ver) VALUES ($1, $2, 'R4', false, $3, $3, $4, $5, $6, $7, $8, 130, $9)`,
			pid, deleted, now, enc, lo, resID, text, v.typ, v.vid); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE hfj_res_ver SET request_id = $2 WHERE pid = $1`, pid, v.reqID); err != nil {
			return err
		}
		if known {
			if _, err := tx.Exec(ctx, `UPDATE hfj_resource SET res_ver = $2, res_updated = $3, res_deleted_at = $4 WHERE res_id = $1`,
				resID, v.vid, now, deleted); err != nil {
				return err
			}
		} else if _, err := tx.Exec(ctx, `INSERT INTO hfj_resource (res_id, res_version, res_published, res_updated, fhir_id, res_type, res_type_id, res_ver)
		    VALUES ($1, 'R4', $2, $2, $3, $4, 130, $5)`, resID, now, v.id, v.typ, v.vid); err != nil {
			return err
		}
		if v.res != nil {
			if _, err := tx.Exec(ctx, `INSERT INTO hfj_spidx_token (sp_id, sp_name, res_id, res_type, sp_value) VALUES (nextval('fhirtest_pid'), 'status', $1, $2, $3)`,
				resID, v.typ, fmt.Sprint(v.res["status"])); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}

// placeholder is the Patient a create names that the server doesn't know (PlaceholderRefs).
func (s *Server) placeholder(res map[string]any) (string, bool) {
	if !s.PlaceholderRefs || s.db == nil {
		return "", false
	}
	sub, _ := res["subject"].(map[string]any)
	ref, _ := sub["reference"].(string)
	id, ok := strings.CutPrefix(ref, "Patient/")
	if !ok || id == "" || len(s.versions["Patient/"+id]) > 0 {
		return "", false
	}
	return id, true
}
