package main

import "github.com/this-is-tobi/rta/pkg/findings"

// The controls keycloak.audit cites. Every one was verified rather than
// recalled, the discipline pkg/findings.Reference asks for: the CWE titles
// against cwe.mitre.org, the OWASP categories against top10.owasp.org/2025
// (through the SDK's constants), the RFC 9700 sections against the RFC as
// published by the RFC Editor, and the two Keycloak citations against the
// current documentation at keycloak.org — the master-realm sentence is in
// the Server Administration Guide under "The master realm", and the
// temporary-admin one is the "Bootstrapping and recovering an admin
// account" page of the server guide. A hardening tool that cites the wrong
// control is worse than one that cites none, because the wrong one still
// reads as authoritative.

// rfc9700 is the RFC Editor's HTML rendering, whose section anchors are
// stable — and were checked, one by one, before being written here.
const rfc9700 = "https://www.rfc-editor.org/rfc/rfc9700.html"

var (
	refMFA = findings.Reference{OWASP: findings.OWASPAuth, CWE: "CWE-308",
		Title: "Use of Single-factor Authentication"}
	refBruteForce = findings.Reference{OWASP: findings.OWASPAuth, CWE: "CWE-307",
		Title: "Improper Restriction of Excessive Authentication Attempts"}
	refPasswords = findings.Reference{OWASP: findings.OWASPAuth, CWE: "CWE-521",
		Title: "Weak Password Requirements"}
	refSession = findings.Reference{OWASP: findings.OWASPAuth, CWE: "CWE-613",
		Title: "Insufficient Session Expiration"}
	refRedirect = findings.Reference{OWASP: findings.OWASPAccessControl, CWE: "CWE-601",
		Title: "URL Redirection to Untrusted Site ('Open Redirect')"}
	refCORS = findings.Reference{OWASP: findings.OWASPMisconfig, CWE: "CWE-942",
		Title: "Permissive Cross-domain Policy with Untrusted Domains"}
	refCleartext = findings.Reference{OWASP: findings.OWASPCrypto, CWE: "CWE-319",
		Title: "Cleartext Transmission of Sensitive Information"}
	refLogging = findings.Reference{OWASP: findings.OWASPLogging, CWE: "CWE-778",
		Title: "Insufficient Logging"}
	// A service account that can administer its realm, and an account
	// holding realm-admin, are principals with more authority than their
	// task needs — the same weakness rta's own audit cites for an agent
	// allowed every tool. Full scope is the milder cousin: authority
	// granted to a token rather than to a principal.
	refExcessivePriv = findings.Reference{OWASP: findings.OWASPAccessControl, CWE: "CWE-250",
		Title: "Execution with Unnecessary Privileges"}
	refPrivilege = findings.Reference{OWASP: findings.OWASPAccessControl, CWE: "CWE-269",
		Title: "Improper Privilege Management"}

	// The OAuth 2.0 Security Best Current Practice is the control for what
	// a client may do, and it is cited by section because it says each of
	// these in one sentence. Source/Control shape: an RFC has no CWE, and
	// the link is the RFC Editor's own HTML with the section anchors it
	// publishes.
	refImplicit = findings.Reference{Source: "RFC 9700", Control: "§2.1.2",
		Title: "Clients SHOULD NOT use the implicit grant or other response types issuing access tokens in the authorization response",
		Link:  rfc9700 + "#section-2.1.2"}
	refROPC = findings.Reference{Source: "RFC 9700", Control: "§2.4",
		Title: "The resource owner password credentials grant MUST NOT be used", Link: rfc9700 + "#section-2.4"}
	refPKCE = findings.Reference{Source: "RFC 9700", Control: "§2.1.1",
		Title: "Public clients MUST use PKCE", Link: rfc9700 + "#section-2.1.1"}
	refRefresh = findings.Reference{Source: "RFC 9700", Control: "§2.2.2",
		Title: "Refresh tokens for public clients MUST be sender-constrained or use refresh token rotation",
		Link:  rfc9700 + "#section-2.2.2"}

	// Keycloak's own guidance, for the two findings that are about how
	// Keycloak is meant to be run rather than about a weakness class.
	refMasterRealm = findings.Reference{Source: "Keycloak Server Administration Guide", Control: "The master realm",
		Title: "Use the master realm only to create and manage the realms in your system",
		Link:  "https://www.keycloak.org/docs/latest/server_admin/index.html#the-master-realm"}
	refBootstrapAdmin = findings.Reference{Source: "Keycloak server guide", Control: "Bootstrapping and recovering an admin account",
		Title: "A bootstrap admin account is temporary and should exist only for the duration necessary to gain " +
			"permanent and more secure admin access; after that it needs to be removed manually",
		Link: "https://www.keycloak.org/server/bootstrap-admin-recovery"}
)
