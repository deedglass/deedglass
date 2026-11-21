# Deedglass Kubernetes Deployment Packaging

This directory contains deployment packaging for single-tenant enterprise and hospital environments:

- [`deedglass`](./deedglass): Official production Helm chart for deploying Deedglass on Kubernetes clusters with external PostgreSQL logical replication, Hospital OIDC SSO, and zero outbound network egress policies (`P-093`).

For usage instructions and configuration presets, refer to [deploy/helm/deedglass/README.md](./deedglass/README.md).
