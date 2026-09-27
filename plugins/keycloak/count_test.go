package main

import (
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/findings"
)

// A count of one reads in the singular wherever the audit prints one. These
// are the three a realm setting or --max could bring to one and that said
// "1 failures", "1 times" and "the first 1 users were".

func TestALockoutAfterOneFailureIsInTheSingular(t *testing.T) {
	r := &findings.Report{}
	auditBruteForce(r, realmRep{BruteForceProtected: true, FailureFactor: 1, PermanentLockout: true})
	expect(t, graded(t, r.Table(false)), "detection", findings.OK, "locks after 1 failure,")
}

func TestOneRefreshTokenReuseIsInTheSingular(t *testing.T) {
	r := &findings.Report{}
	auditTokens(r, realmRep{RevokeRefreshToken: true, RefreshTokenMaxReuse: 1})
	expect(t, graded(t, r.Table(false)), "refresh-rotation", findings.OK, "reuse allowed 1 time)")
}

func TestAnAuditBoundToOneUserSaysSoInTheSingular(t *testing.T) {
	f := newFakeKeycloak(t)
	got := graded(t, run(t, f, "keycloak.audit", map[string]any{"detail": true, "max": 1}))
	g, ok := got["coverage-bound"]
	if !ok {
		t.Fatal("an audit bound to one user did not say it stopped there")
	}
	if !strings.HasPrefix(g.detail, "only the first 1 user was examined") {
		t.Errorf("coverage-bound detail = %q, want the one user counted in the singular", g.detail)
	}
}
