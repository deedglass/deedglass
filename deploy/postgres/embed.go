// Package postgres embeds the control-plane roles script, so deedglass init creates the roles with
// the same SQL as deploy/docker-compose.yml.
package postgres

import _ "embed"

// ControlPlaneRoles is control-plane-roles.sql (psql variables migrator_password, app_password).
//
//go:embed control-plane-roles.sql
var ControlPlaneRoles string
