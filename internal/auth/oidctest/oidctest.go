// Package oidctest is a fake OIDC identity provider for internal/auth's
// integration tests: discovery, JWKS and a token endpoint that issues an
// RS256 ID token with claims the test controls. It has no /authorize
// endpoint - tests drive the callback directly with a code the fake token
// endpoint always accepts - so it only covers what Task 4's callback flow
// actually exercises.
package oidctest

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

const keyID = "test-key"

// Claims configures the next ID token the fake token endpoint issues.
// Iss and Aud are filled in automatically (Iss to the server's own URL,
// Aud to the client_id the token request carries) unless overridden.
type Claims struct {
	Sub           string
	Email         string
	EmailVerified bool
	Nonce         string
	// Aud overrides the audience; empty uses the request's client_id.
	Aud string
	// Iss overrides the issuer; empty uses the server's own URL.
	Iss string
	// ExpiresIn defaults to 5 minutes when zero; negative issues an
	// already-expired token.
	ExpiresIn time.Duration
	// EmailVerifiedRaw, if non-nil, replaces the email_verified claim's
	// value outright (e.g. the string "true" instead of the JSON boolean
	// true), overriding EmailVerified - for testing that a non-boolean
	// claim is rejected rather than coerced.
	EmailVerifiedRaw any
}

// Server is a fake OIDC provider.
type Server struct {
	*httptest.Server

	key *rsa.PrivateKey

	mu           sync.Mutex
	claims       Claims
	lastVerifier string
}

// New starts a fake OIDC provider. Call SetClaims before driving a callback
// to control the ID token it issues.
func New(t interface{ Cleanup(func()) }) *Server {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	s := &Server{key: key}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", s.handleDiscovery)
	mux.HandleFunc("/jwks", s.handleJWKS)
	mux.HandleFunc("/token", s.handleToken)
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

// SetClaims sets the claims the next /token response's ID token carries.
func (s *Server) SetClaims(c Claims) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claims = c
}

// LastCodeVerifier returns the PKCE code_verifier the most recent /token
// request carried, so a test can assert PKCE was actually sent.
func (s *Server) LastCodeVerifier() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastVerifier
}

func (s *Server) handleDiscovery(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"issuer":                                s.URL,
		"authorization_endpoint":                s.URL + "/authorize",
		"token_endpoint":                        s.URL + "/token",
		"jwks_uri":                              s.URL + "/jwks",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"scopes_supported":                      []string{"openid", "email", "profile"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"},
		"claims_supported":                      []string{"sub", "email", "email_verified"},
		"code_challenge_methods_supported":      []string{"S256"},
	})
}

func (s *Server) handleJWKS(w http.ResponseWriter, r *http.Request) {
	jwk := jose.JSONWebKey{Key: &s.key.PublicKey, KeyID: keyID, Algorithm: string(jose.RS256), Use: "sig"}
	set := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(set)
}

// handleToken accepts any authorization code (there's no /authorize step to
// check it against) and returns an ID token built from the claims a test
// last set via SetClaims.
func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	s.lastVerifier = r.PostForm.Get("code_verifier")
	c := s.claims
	s.mu.Unlock()

	clientID := r.PostForm.Get("client_id")
	if clientID == "" {
		// Confidential clients sent via HTTP Basic auth rather than the
		// body - oauth2.Config does this when a client secret is set.
		clientID, _, _ = r.BasicAuth()
	}

	aud := c.Aud
	if aud == "" {
		aud = clientID
	}
	iss := c.Iss
	if iss == "" {
		iss = s.URL
	}
	expiresIn := c.ExpiresIn
	if expiresIn == 0 {
		expiresIn = 5 * time.Minute
	}

	emailVerified := any(c.EmailVerified)
	if c.EmailVerifiedRaw != nil {
		emailVerified = c.EmailVerifiedRaw
	}

	now := time.Now()
	claims := map[string]any{
		"iss":            iss,
		"aud":            aud,
		"sub":            c.Sub,
		"email":          c.Email,
		"email_verified": emailVerified,
		"iat":            now.Unix(),
		"exp":            now.Add(expiresIn).Unix(),
	}
	if c.Nonce != "" {
		claims["nonce"] = c.Nonce
	}

	idToken, err := s.sign(claims)
	if err != nil {
		http.Error(w, "signing failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": "test-access-token",
		"token_type":   "Bearer",
		"id_token":     idToken,
		"expires_in":   int(expiresIn.Seconds()),
	})
}

func (s *Server) sign(claims map[string]any) (string, error) {
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: s.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", keyID),
	)
	if err != nil {
		return "", err
	}
	return jwt.Signed(signer).Claims(claims).Serialize()
}
