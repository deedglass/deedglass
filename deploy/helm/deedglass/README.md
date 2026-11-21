# Deedglass Hospital Helm Chart

Production Helm chart for deploying Deedglass as a single-tenant physical storage witness and zero-PHI cryptographic provenance ledger on hospital Kubernetes clusters (on-premise data centers, private cloud VPCs, or sovereign cloud enclaves).

## Architecture & Boundary Overview

Deedglass operates strictly as non-SaMD administrative and data-integrity software. It provides:
1. **L1 & L2 Physical Storage Witnessing:** Correlates FHIR API responses and PostgreSQL logical WAL changes to detect silent divergence without storing plaintext Protected Health Information (PHI).
2. **Zero-PHI Ledgers (`phimode`):** Clinical identifiers and subjects are processed through salted SHA-256 cryptographic digests before recording actions.
3. **Single-Tenant Hospital SSO:** Direct OpenID Connect (OIDC) integration with hospital identity providers (Microsoft Entra ID, Okta, Google Workspace) mapping active directory groups to clinical roles and duty rota profiles.
4. **Zero Outbound Telemetry (`P-093`):** Strict network policies preventing external outbound network egress; all deeds, audit records, and logs remain entirely within the hospital's private boundary.

## Prerequisites

- Kubernetes 1.28+
- Helm 3.12+
- External PostgreSQL 16, 17, or 18 with logical decoding enabled:
  ```sql
  -- postgresql.conf requirements for L2 storage witness
  wal_level = logical
  max_replication_slots = 10
  max_wal_senders = 10
  ```
- Hospital ingress controller with TLS termination (e.g., ingress-nginx, cert-manager)

## Quick Start

### 1. Add Configuration Values

Create a custom `hospital-values.yaml`:

```yaml
baseUrl: "https://deedglass.hospital.internal"
listen: "0.0.0.0:8080"
metricsListen: "0.0.0.0:9464"

database:
  mode: "external"
  host: "postgres.hospital.internal"
  port: 5432
  database: "deedglass"
  user: "deedglass_app"
  passwordExistingSecret: "hospital-postgres-credentials"
  passwordKey: "app-password"
  migrateUser: "deedglass_migrator"
  migratePasswordExistingSecret: "hospital-postgres-credentials"
  migratePasswordKey: "migrator-password"
  sslmode: "require"

phimode:
  enabled: true
  mode: "digest"

oidc:
  enabled: true
  providerType: "entra_id"
  displayName: "Hospital Azure AD"
  issuer: "https://login.microsoftonline.com/YOUR_TENANT_ID/v2.0"
  clientId: "YOUR_APPLICATION_CLIENT_ID"
  clientSecretExistingSecret: "hospital-oidc-credentials"
  clientSecretKey: "client-secret"
  redirectUri: "https://deedglass.hospital.internal/app/sso/callback"
  roleMapping:
    groupsClaim: "groups"
    ownerGroups:
      - "Hospital-SecOps-Admins"
    adminGroups:
      - "Clinical-Informatics-Leads"
      - "Emergency-Department-HODs"
    viewerGroups:
      - "Clinicians"
      - "Audit-Committee"
    defaultRole: "viewer"
    failClosed: true
    dutyRota:
      "Emergency-Consultants":
        ward: "Emergency Department"
        role_title: "Attending Consultant"
        can_break_glass: true
        require_four_eyes: false

networkPolicy:
  enabled: true
  allowDNS: true
  allowPostgres: true
  postgresPort: 5432
  allowOIDC: true
```

### 2. Install the Chart

```bash
helm install deedglass ./deploy/helm/deedglass \
  --namespace deedglass \
  --create-namespace \
  --values hospital-values.yaml
```

The pre-install hook runs `deedglass migrate up` to apply database migrations before starting the main service.

### 3. Verification

Check deployment status and health endpoints:

```bash
# Verify pods are running and healthy
kubectl get pods -n deedglass

# Inspect health check endpoint
kubectl exec -n deedglass deployment/deedglass -- wget -qO- http://127.0.0.1:8080/v1/health

# Run chart automated verification test
helm test deedglass -n deedglass
```

## Security Profile

- **Read-Only Root Filesystem:** Container runs with `readOnlyRootFilesystem: true`.
- **Non-Root Execution:** Dedicated non-privileged user UID 65532 (`runAsNonRoot: true`).
- **Capability Dropping:** All Linux kernel capabilities dropped (`drop: [ALL]`).
- **Privilege Escalation:** Disabled (`allowPrivilegeEscalation: false`).
- **Seccomp Profile:** `RuntimeDefault`.
