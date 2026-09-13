package main

import (
	"strconv"
	"time"
)

// The Admin REST representations this plugin reads, each declaring only the
// fields it renders or grades. Every one was checked against a Keycloak
// 26.7 answering the real endpoint rather than against the documentation:
// which fields a service account with view-* roles actually receives, and
// which it does not (`keycloakVersion` on a realm is null; the version
// lives in /admin/serverinfo and only a master-realm client sees it).
//
// **clientRep has no field for the secret, on purpose.** A client
// representation carries `secret` for every confidential client, to anyone
// holding view-clients — which is the role this plugin needs for everything
// else it says about a client. Decoding into a struct that has no such
// field is how the value never becomes a typed thing any code path here
// could render, log or return; and no capability reads the /client-secret
// endpoint. Attributes are the other place a client keeps key material (a
// SAML client's signing key lives in `saml.signing.private.key`), which is
// why client.show renders an allowlist of them rather than the map.

type serverInfo struct {
	SystemInfo struct {
		Version string `json:"version"`
	} `json:"systemInfo"`
}

type realmRep struct {
	Realm       string `json:"realm"`
	DisplayName string `json:"displayName"`
	Enabled     bool   `json:"enabled"`

	SSLRequired                 string `json:"sslRequired"`
	RegistrationAllowed         bool   `json:"registrationAllowed"`
	RegistrationEmailAsUsername bool   `json:"registrationEmailAsUsername"`
	VerifyEmail                 bool   `json:"verifyEmail"`
	LoginWithEmailAllowed       bool   `json:"loginWithEmailAllowed"`
	DuplicateEmailsAllowed      bool   `json:"duplicateEmailsAllowed"`
	ResetPasswordAllowed        bool   `json:"resetPasswordAllowed"`
	EditUsernameAllowed         bool   `json:"editUsernameAllowed"`
	RememberMe                  bool   `json:"rememberMe"`

	BruteForceProtected          bool   `json:"bruteForceProtected"`
	BruteForceStrategy           string `json:"bruteForceStrategy"`
	PermanentLockout             bool   `json:"permanentLockout"`
	FailureFactor                int    `json:"failureFactor"`
	WaitIncrementSeconds         int    `json:"waitIncrementSeconds"`
	MaxFailureWaitSeconds        int    `json:"maxFailureWaitSeconds"`
	MaxDeltaTimeSeconds          int    `json:"maxDeltaTimeSeconds"`
	MinimumQuickLoginWaitSeconds int    `json:"minimumQuickLoginWaitSeconds"`
	MaxTemporaryLockouts         int    `json:"maxTemporaryLockouts"`

	PasswordPolicy     string `json:"passwordPolicy"`
	OTPPolicyType      string `json:"otpPolicyType"`
	OTPPolicyAlgorithm string `json:"otpPolicyAlgorithm"`
	OTPPolicyDigits    int    `json:"otpPolicyDigits"`

	EventsEnabled             bool     `json:"eventsEnabled"`
	EventsExpiration          int64    `json:"eventsExpiration"`
	EventsListeners           []string `json:"eventsListeners"`
	EnabledEventTypes         []string `json:"enabledEventTypes"`
	AdminEventsEnabled        bool     `json:"adminEventsEnabled"`
	AdminEventsDetailsEnabled bool     `json:"adminEventsDetailsEnabled"`

	AccessTokenLifespan                int  `json:"accessTokenLifespan"`
	AccessTokenLifespanForImplicitFlow int  `json:"accessTokenLifespanForImplicitFlow"`
	SSOSessionIdleTimeout              int  `json:"ssoSessionIdleTimeout"`
	SSOSessionMaxLifespan              int  `json:"ssoSessionMaxLifespan"`
	ClientSessionIdleTimeout           int  `json:"clientSessionIdleTimeout"`
	ClientSessionMaxLifespan           int  `json:"clientSessionMaxLifespan"`
	OfflineSessionIdleTimeout          int  `json:"offlineSessionIdleTimeout"`
	OfflineSessionMaxLifespanEnabled   bool `json:"offlineSessionMaxLifespanEnabled"`
	OfflineSessionMaxLifespan          int  `json:"offlineSessionMaxLifespan"`
	RevokeRefreshToken                 bool `json:"revokeRefreshToken"`
	RefreshTokenMaxReuse               int  `json:"refreshTokenMaxReuse"`

	DefaultSignatureAlgorithm string `json:"defaultSignatureAlgorithm"`
	BrowserFlow               string `json:"browserFlow"`
	DirectGrantFlow           string `json:"directGrantFlow"`
	RegistrationFlow          string `json:"registrationFlow"`
	ResetCredentialsFlow      string `json:"resetCredentialsFlow"`
	ClientAuthenticationFlow  string `json:"clientAuthenticationFlow"`
}

type userRep struct {
	ID                     string              `json:"id"`
	Username               string              `json:"username"`
	Email                  string              `json:"email"`
	FirstName              string              `json:"firstName"`
	LastName               string              `json:"lastName"`
	Enabled                bool                `json:"enabled"`
	EmailVerified          bool                `json:"emailVerified"`
	TOTP                   bool                `json:"totp"`
	CreatedTimestamp       int64               `json:"createdTimestamp"`
	RequiredActions        []string            `json:"requiredActions"`
	FederationLink         string              `json:"federationLink"`
	ServiceAccountClientID string              `json:"serviceAccountClientId"`
	Attributes             map[string][]string `json:"attributes"`
}

type credentialRep struct {
	Type        string `json:"type"`
	UserLabel   string `json:"userLabel"`
	CreatedDate int64  `json:"createdDate"`
}

type roleRep struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Composite   bool   `json:"composite"`
	ClientRole  bool   `json:"clientRole"`
}

type roleMappings struct {
	RealmMappings  []roleRep `json:"realmMappings"`
	ClientMappings map[string]struct {
		Client   string    `json:"client"`
		Mappings []roleRep `json:"mappings"`
	} `json:"clientMappings"`
}

type groupRep struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type clientRep struct {
	ID                        string            `json:"id"`
	ClientID                  string            `json:"clientId"`
	Name                      string            `json:"name"`
	Description               string            `json:"description"`
	Protocol                  string            `json:"protocol"`
	Enabled                   bool              `json:"enabled"`
	PublicClient              bool              `json:"publicClient"`
	BearerOnly                bool              `json:"bearerOnly"`
	StandardFlowEnabled       bool              `json:"standardFlowEnabled"`
	ImplicitFlowEnabled       bool              `json:"implicitFlowEnabled"`
	DirectAccessGrantsEnabled bool              `json:"directAccessGrantsEnabled"`
	ServiceAccountsEnabled    bool              `json:"serviceAccountsEnabled"`
	FullScopeAllowed          bool              `json:"fullScopeAllowed"`
	ConsentRequired           bool              `json:"consentRequired"`
	RootURL                   string            `json:"rootUrl"`
	BaseURL                   string            `json:"baseUrl"`
	RedirectURIs              []string          `json:"redirectUris"`
	WebOrigins                []string          `json:"webOrigins"`
	DefaultClientScopes       []string          `json:"defaultClientScopes"`
	OptionalClientScopes      []string          `json:"optionalClientScopes"`
	ClientAuthenticatorType   string            `json:"clientAuthenticatorType"`
	Attributes                map[string]string `json:"attributes"`
}

// kind is the one word an operator uses for a client's shape.
func (c clientRep) kind() string {
	switch {
	case c.BearerOnly:
		return "bearer-only"
	case c.PublicClient:
		return "public"
	}
	return "confidential"
}

// pkce is the code challenge method a client enforces, or "" for none.
func (c clientRep) pkce() string { return c.Attributes["pkce.code.challenge.method"] }

type flowRep struct {
	ID          string `json:"id"`
	Alias       string `json:"alias"`
	Description string `json:"description"`
	ProviderID  string `json:"providerId"`
	TopLevel    bool   `json:"topLevel"`
	BuiltIn     bool   `json:"builtIn"`
}

type executionRep struct {
	ID                 string `json:"id"`
	DisplayName        string `json:"displayName"`
	ProviderID         string `json:"providerId"`
	Requirement        string `json:"requirement"`
	Level              int    `json:"level"`
	Index              int    `json:"index"`
	AuthenticationFlow bool   `json:"authenticationFlow"`
}

type requiredActionRep struct {
	Alias         string `json:"alias"`
	Name          string `json:"name"`
	Enabled       bool   `json:"enabled"`
	DefaultAction bool   `json:"defaultAction"`
}

type eventsConfig struct {
	EventsEnabled             bool     `json:"eventsEnabled"`
	EventsExpiration          int64    `json:"eventsExpiration"`
	EventsListeners           []string `json:"eventsListeners"`
	EnabledEventTypes         []string `json:"enabledEventTypes"`
	AdminEventsEnabled        bool     `json:"adminEventsEnabled"`
	AdminEventsDetailsEnabled bool     `json:"adminEventsDetailsEnabled"`
}

type eventRep struct {
	Time      int64             `json:"time"`
	Type      string            `json:"type"`
	ClientID  string            `json:"clientId"`
	UserID    string            `json:"userId"`
	IPAddress string            `json:"ipAddress"`
	Error     string            `json:"error"`
	Details   map[string]string `json:"details"`
}

type adminEventRep struct {
	Time          int64  `json:"time"`
	OperationType string `json:"operationType"`
	ResourceType  string `json:"resourceType"`
	ResourcePath  string `json:"resourcePath"`
	Error         string `json:"error"`
	AuthDetails   struct {
		UserID    string `json:"userId"`
		ClientID  string `json:"clientId"`
		IPAddress string `json:"ipAddress"`
	} `json:"authDetails"`
}

// clientSessionStat is what /client-session-stats returns: counts as
// strings, which is Keycloak's choice and not worth a conversion that could
// only fail.
type clientSessionStat struct {
	ClientID string `json:"clientId"`
	Active   string `json:"active"`
	Offline  string `json:"offline"`
}

type userSessionRep struct {
	ID         string            `json:"id"`
	Username   string            `json:"username"`
	IPAddress  string            `json:"ipAddress"`
	Start      int64             `json:"start"`
	LastAccess int64             `json:"lastAccess"`
	Clients    map[string]string `json:"clients"`
}

// stamp renders one of Keycloak's millisecond timestamps for a
// KindTimestamp column; zero, which the API uses for "never", renders empty.
func stamp(ms int64) string {
	if ms == 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

// yesNo is the cell for a boolean setting.
func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// span renders one of the realm's lifespans, which Keycloak keeps in
// seconds, in the unit a person would use: 30d rather than 720h0m0s,
// because a session policy is set in days and read in days.
func span(n int) string {
	switch {
	case n <= 0:
		return "0s"
	case n%86400 == 0:
		return strconv.Itoa(n/86400) + "d"
	case n%3600 == 0:
		return strconv.Itoa(n/3600) + "h"
	case n%60 == 0:
		return strconv.Itoa(n/60) + "m"
	}
	return (time.Duration(n) * time.Second).String()
}

func itoa(n int) string { return strconv.Itoa(n) }
