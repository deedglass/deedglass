//go:build !attackhooks

package main

import "testing"

// TestNoAttackHooks: the attack harness's resolver exists only in a -tags attackhooks build.
func TestNoAttackHooks(t *testing.T) {
	if egressResolverHook != nil {
		t.Fatal("a default build carries the attack harness's resolver hook")
	}
}
