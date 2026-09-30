//go:build integration

package migrate

// SetAppRoleForTest makes preflight look for another runtime role, so the missing-role refusal can
// be tested without dropping a cluster-wide role. Compiled only with the integration build tag.
func SetAppRoleForTest(name string) (restore func()) {
	old := appRole
	appRole = name
	return func() { appRole = old }
}
