package main

import (
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/store/policystore"
)

// S12 §3.10: org creation enforces default-deny inside its own transaction, so no org exists
// without an enforced policy. Every path that creates an org (deedglass org create, and the
// server's own paths) runs identity.CreateOrg, which runs these hooks.
func init() {
	identity.OrgCreateHooks = append(identity.OrgCreateHooks, policystore.BootstrapOrg)
}
