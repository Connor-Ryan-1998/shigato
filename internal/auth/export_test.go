//go:build integration

package auth

// Re-exports for the integration tests in package auth_test, which live
// outside package auth so they can use internal/testkit (which imports
// auth).

var (
	CreateSession  = createSession
	UserIDForToken = userIDForToken
	HashToken      = hashToken
)

const (
	OIDCStateCookieName = oidcStateCookieName
	LoginEmailLimit     = loginEmailLimit
	LoginIPLimit        = loginIPLimit
)
