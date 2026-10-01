package setup

import (
	"net"
	"strings"
)

// Provider is where a database runs, as far as setup can tell. Guidance differs per provider;
// "guidance only" providers have not been smoke-tested (docs/CLOUD_COMPATIBILITY.md).
type Provider string

const (
	ProviderLocal    Provider = "local" // localhost, 127.0.0.1, ::1 or a Unix socket (Docker looks the same)
	ProviderRDS      Provider = "rds"
	ProviderAurora   Provider = "aurora"
	ProviderSupabase Provider = "supabase"
	ProviderNeon     Provider = "neon"
	ProviderCloudSQL Provider = "cloudsql"
	ProviderAzure    Provider = "azure"
	ProviderOther    Provider = "postgres" // a self-hosted server on another host
)

// Label is the provider as a person reads it.
func (p Provider) Label() string {
	switch p {
	case ProviderLocal:
		return "local"
	case ProviderRDS:
		return "Amazon RDS"
	case ProviderAurora:
		return "Amazon Aurora"
	case ProviderSupabase:
		return "Supabase"
	case ProviderNeon:
		return "Neon"
	case ProviderCloudSQL:
		return "Google Cloud SQL"
	case ProviderAzure:
		return "Azure Database for PostgreSQL"
	}
	return "self-hosted"
}

// IsLocalHost reports whether host is this machine: localhost, a loopback address, or a Unix socket
// path (empty or starting with /).
func IsLocalHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "" || strings.HasPrefix(h, "/") || h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(h, "[]"))
	return ip != nil && ip.IsLoopback()
}

// Detect tells the provider from the host and what the catalogs show (S17 §3.10).
func Detect(host string, st *State) Provider {
	h := strings.ToLower(host)
	has := func(role string) bool {
		if st == nil {
			return false
		}
		for _, r := range st.ProviderRoles {
			if r == role {
				return true
			}
		}
		return false
	}
	switch {
	case st != nil && st.AuroraVersion:
		return ProviderAurora
	case st != nil && hasSetting(st, "rds.logical_replication") || has("rds_superuser") || strings.Contains(h, ".rds.amazonaws.com"):
		if strings.Contains(h, ".cluster-") {
			return ProviderAurora
		}
		return ProviderRDS
	case has("supabase_admin") || strings.HasSuffix(h, ".supabase.co") || strings.HasSuffix(h, ".supabase.com"):
		return ProviderSupabase
	case st != nil && st.NeonSettings || has("neon_superuser") || strings.HasSuffix(h, ".neon.tech"):
		return ProviderNeon
	case st != nil && hasSetting(st, "cloudsql.logical_decoding") || has("cloudsqlsuperuser"):
		return ProviderCloudSQL
	case has("azure_pg_admin") || strings.HasSuffix(h, ".postgres.database.azure.com"):
		return ProviderAzure
	case IsLocalHost(host):
		return ProviderLocal
	}
	return ProviderOther
}

func hasSetting(st *State, name string) bool {
	_, ok := st.Settings[name]
	return ok
}

// WALLevelFix is the provider's way to turn on logical decoding, one or two sentences.
func WALLevelFix(p Provider) (fix, fixSQL string) {
	switch p {
	case ProviderLocal, ProviderOther:
		return "Set wal_level = logical, then restart Postgres (pg_ctl -D \"$PGDATA\" restart). In Docker, add command: [\"postgres\", \"-c\", \"wal_level=logical\", \"-c\", \"max_slot_wal_keep_size=4GB\"] and run docker compose up -d --force-recreate.",
			"ALTER SYSTEM SET wal_level = logical;"
	case ProviderRDS:
		return "Amazon RDS: set rds.logical_replication = 1 in a custom DB parameter group, then reboot the instance. The parameter is static, and turning it on needs rds_superuser.", ""
	case ProviderAurora:
		return "Amazon Aurora: set rds.logical_replication = 1 in a custom DB cluster parameter group, then reboot the writer instance. The parameter is static.", ""
	case ProviderSupabase:
		return "Supabase databases usually run with wal_level = logical already. If this one doesn't, contact Supabase support.", ""
	case ProviderNeon:
		return "Neon: in the Console, Settings › Postgres › Logical replication › Enable. It restarts the project's computes, drops their connections, and can't be reverted.", ""
	case ProviderCloudSQL:
		return "Google Cloud SQL (guidance only): set the database flag cloudsql.logical_decoding = on (the instance restarts), then give the witness REPLICATION with ALTER USER.", ""
	case ProviderAzure:
		return "Azure Flexible Server (guidance only): set the server parameters wal_level = logical and max_worker_processes to at least 16, save, restart the server, then give the witness REPLICATION with ALTER ROLE.", ""
	}
	return "Set wal_level = logical, then restart Postgres.", ""
}

// DocURL is the provider's guide page.
func DocURL(p Provider) string {
	switch p {
	case ProviderRDS, ProviderAurora:
		return "https://docs.deedglass.com/connectors/postgres/rds"
	case ProviderSupabase:
		return "https://docs.deedglass.com/connectors/postgres/supabase"
	case ProviderNeon:
		return "https://docs.deedglass.com/connectors/postgres/neon"
	}
	return "https://docs.deedglass.com/connectors/postgres"
}
