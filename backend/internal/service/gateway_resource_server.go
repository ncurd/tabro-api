package service

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/sync/singleflight"
)

const (
	gatewayOIDCMaxTokenLength       = 16 * 1024
	gatewayOIDCUnknownKIDRefreshGap = 30 * time.Second
)

var (
	ErrGatewayOIDCTokenInvalid     = errors.New("invalid gateway oauth access token")
	ErrGatewayOIDCScopeDenied      = errors.New("gateway oauth access token has insufficient scope")
	ErrGatewayOIDCIdentityNotBound = errors.New("gateway oauth identity is not bound to a billing account")
	ErrGatewayOIDCUnavailable      = errors.New("gateway oauth resource server is temporarily unavailable")
)

// GatewayOIDCPrincipal contains only claims that have passed signature,
// issuer, audience, time, scope, client and delegation validation.
type GatewayOIDCPrincipal struct {
	Issuer     string
	Subject    string
	Tenant     string
	ClientID   string
	ExpiresAt  time.Time
	Scopes     []string
	ActorChain []string
}

type gatewayOIDCDiscovery struct {
	Issuer  string `json:"issuer"`
	JWKSURL string `json:"jwks_uri"`
}

type gatewayOIDCJWKSet struct {
	Keys []gatewayOIDCJWK `json:"keys"`
}

type gatewayOIDCJWK struct {
	Kty    string   `json:"kty"`
	Kid    string   `json:"kid"`
	Use    string   `json:"use"`
	Alg    string   `json:"alg"`
	KeyOps []string `json:"key_ops"`
	N      string   `json:"n"`
	E      string   `json:"e"`
	Crv    string   `json:"crv"`
	X      string   `json:"x"`
	Y      string   `json:"y"`
}

type gatewayOIDCSigningKey struct {
	key any
	alg string
}

// GatewayResourceServer validates external access tokens intended only for the
// LLM gateway. It never validates locally issued Tabro session/access tokens.
type GatewayResourceServer struct {
	cfg           config.GatewayResourceServerConfig
	apiKeyService *APIKeyService
	httpClient    *http.Client

	mu        sync.RWMutex
	keys      map[string]gatewayOIDCSigningKey
	expiresAt time.Time
	jwksURL   string
	// unknownKIDRefreshAfter rate-limits attacker-controlled JWKS refreshes.
	// It is set only after a refresh still cannot resolve a kid, so ordinary
	// signing-key rotation gets one immediate refresh opportunity.
	unknownKIDRefreshAfter time.Time
	refresh                singleflight.Group
}

func NewGatewayResourceServer(cfg *config.Config, apiKeyService *APIKeyService) *GatewayResourceServer {
	rsCfg := config.GatewayResourceServerConfig{}
	if cfg != nil {
		rsCfg = cfg.Gateway.ResourceServer
	}
	return newGatewayResourceServerWithClient(rsCfg, apiKeyService, &http.Client{Timeout: 10 * time.Second})
}

func newGatewayResourceServerWithClient(cfg config.GatewayResourceServerConfig, apiKeyService *APIKeyService, client *http.Client) *GatewayResourceServer {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	client = gatewayOIDCHardenedHTTPClient(client, cfg.IssuerURL)
	return &GatewayResourceServer{
		cfg:           cfg,
		apiKeyService: apiKeyService,
		httpClient:    client,
		keys:          make(map[string]gatewayOIDCSigningKey),
	}
}

// gatewayOIDCHardenedHTTPClient validates every redirect hop used to fetch
// discovery/JWKS metadata. Checking only resp.Request.URL after the request is
// insufficient: an HTTPS -> HTTP -> HTTPS chain could otherwise expose the
// redirect decision to an on-path attacker before returning to TLS.
func gatewayOIDCHardenedHTTPClient(client *http.Client, issuer string) *http.Client {
	clone := *client
	previousCheckRedirect := clone.CheckRedirect
	clone.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req == nil || req.URL == nil {
			return errors.New("oidc metadata redirect is missing its url")
		}
		if err := gatewayOIDCValidateEndpointForIssuer(issuer, req.URL.String()); err != nil {
			return err
		}
		if previousCheckRedirect != nil {
			return previousCheckRedirect(req, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 oidc metadata redirects")
		}
		return nil
	}
	return &clone
}

func (s *GatewayResourceServer) Enabled() bool {
	return s != nil && s.cfg.Enabled
}

// Authenticate verifies the bearer token and resolves its stable (iss, sub)
// identity to an explicitly bound internal billing/routing key.
func (s *GatewayResourceServer) Authenticate(ctx context.Context, rawToken string) (*APIKey, *GatewayOIDCPrincipal, error) {
	principal, err := s.Verify(ctx, rawToken)
	if err != nil {
		return nil, nil, err
	}
	if s.apiKeyService == nil {
		return nil, nil, ErrGatewayOIDCUnavailable
	}
	apiKey, err := s.apiKeyService.GetOIDCGatewayKeyByIdentity(ctx, principal.Issuer, principal.Subject)
	if err != nil {
		if errors.Is(err, ErrAPIKeyNotFound) {
			return nil, nil, ErrGatewayOIDCIdentityNotBound
		}
		return nil, nil, fmt.Errorf("%w: resolve billing identity", ErrGatewayOIDCUnavailable)
	}
	return apiKey, principal, nil
}

// Verify performs the complete OAuth Resource Server validation policy.
func (s *GatewayResourceServer) Verify(ctx context.Context, rawToken string) (*GatewayOIDCPrincipal, error) {
	if s == nil || !s.cfg.Enabled {
		return nil, ErrGatewayOIDCTokenInvalid
	}
	rawToken = strings.TrimSpace(rawToken)
	if rawToken == "" || len(rawToken) > gatewayOIDCMaxTokenLength || strings.Count(rawToken, ".") != 2 {
		return nil, ErrGatewayOIDCTokenInvalid
	}

	validMethods := splitGatewayOIDCList(s.cfg.AllowedSigningAlgs)
	if len(validMethods) == 0 {
		return nil, ErrGatewayOIDCUnavailable
	}
	claims := jwt.MapClaims{}
	parserOptions := []jwt.ParserOption{
		jwt.WithValidMethods(validMethods),
		jwt.WithIssuer(strings.TrimSpace(s.cfg.IssuerURL)),
		jwt.WithAudience(strings.TrimSpace(s.cfg.Audience)),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(time.Duration(s.cfg.ClockSkewSeconds) * time.Second),
	}
	token, err := jwt.ParseWithClaims(rawToken, claims, func(token *jwt.Token) (any, error) {
		kid, _ := token.Header["kid"].(string)
		kid = strings.TrimSpace(kid)
		if kid == "" {
			return nil, ErrGatewayOIDCTokenInvalid
		}
		alg := strings.TrimSpace(token.Method.Alg())
		key, keyErr := s.signingKey(ctx, kid, alg)
		if keyErr != nil {
			return nil, keyErr
		}
		return key, nil
	}, parserOptions...)
	if err != nil {
		if errors.Is(err, ErrGatewayOIDCUnavailable) {
			return nil, ErrGatewayOIDCUnavailable
		}
		return nil, ErrGatewayOIDCTokenInvalid
	}
	if token == nil || !token.Valid {
		return nil, ErrGatewayOIDCTokenInvalid
	}

	issuer, err := claims.GetIssuer()
	if err != nil || issuer != strings.TrimSpace(s.cfg.IssuerURL) {
		return nil, ErrGatewayOIDCTokenInvalid
	}
	audiences, err := claims.GetAudience()
	if err != nil || len(audiences) != 1 || audiences[0] != strings.TrimSpace(s.cfg.Audience) {
		return nil, ErrGatewayOIDCTokenInvalid
	}
	subject, err := claims.GetSubject()
	if err != nil || strings.TrimSpace(subject) == "" {
		return nil, ErrGatewayOIDCTokenInvalid
	}
	subject = strings.TrimSpace(subject)
	expiresAt, err := claims.GetExpirationTime()
	if err != nil || expiresAt == nil || expiresAt.Time.IsZero() {
		return nil, ErrGatewayOIDCTokenInvalid
	}

	clientID, err := gatewayOIDCClientID(claims)
	if err != nil || !gatewayOIDCValueAllowed(clientID, splitGatewayOIDCList(s.cfg.AllowedClientIDs)) {
		return nil, ErrGatewayOIDCTokenInvalid
	}

	tenant := ""
	if claimName := strings.TrimSpace(s.cfg.TenantClaim); claimName != "" {
		tenant, _ = gatewayOIDCStringClaim(claims, claimName)
		tenant = strings.TrimSpace(tenant)
	}
	if s.cfg.RequireTenant && tenant == "" {
		return nil, ErrGatewayOIDCTokenInvalid
	}

	actors, err := s.validateDelegation(claims)
	if err != nil {
		return nil, err
	}

	// Scope is authorization, so evaluate it only after every authentication
	// property above has been validated. A token with an invalid client,
	// tenant, or delegation chain is an invalid token (401), not a valid token
	// that merely lacks permission (403).
	scopes := gatewayOIDCScopes(claims)
	for _, required := range splitGatewayOIDCList(s.cfg.RequiredScopes) {
		if _, ok := scopes[required]; !ok {
			return nil, ErrGatewayOIDCScopeDenied
		}
	}

	return &GatewayOIDCPrincipal{
		Issuer:     issuer,
		Subject:    subject,
		Tenant:     tenant,
		ClientID:   clientID,
		ExpiresAt:  expiresAt.Time,
		Scopes:     gatewayOIDCScopeSlice(scopes),
		ActorChain: actors,
	}, nil
}

func (s *GatewayResourceServer) signingKey(ctx context.Context, kid, alg string) (any, error) {
	now := time.Now()
	if key, ok := s.cachedSigningKey(kid, alg, now); ok {
		return key, nil
	}
	if s.unknownKIDRefreshThrottled(now) {
		return nil, ErrGatewayOIDCTokenInvalid
	}
	_, err, _ := s.refresh.Do("jwks", func() (any, error) {
		return nil, s.refreshSigningKeys(ctx)
	})
	if err != nil {
		return nil, fmt.Errorf("%w: refresh jwks", ErrGatewayOIDCUnavailable)
	}
	if key, ok := s.cachedSigningKey(kid, alg, time.Now()); ok {
		return key, nil
	}
	s.blockUnknownKIDRefresh(time.Now().Add(gatewayOIDCUnknownKIDRefreshGap))
	return nil, ErrGatewayOIDCTokenInvalid
}

func (s *GatewayResourceServer) unknownKIDRefreshThrottled(now time.Time) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return now.Before(s.unknownKIDRefreshAfter)
}

func (s *GatewayResourceServer) blockUnknownKIDRefresh(until time.Time) {
	s.mu.Lock()
	if until.After(s.unknownKIDRefreshAfter) {
		s.unknownKIDRefreshAfter = until
	}
	s.mu.Unlock()
}

func (s *GatewayResourceServer) cachedSigningKey(kid, alg string, now time.Time) (any, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !now.Before(s.expiresAt) {
		return nil, false
	}
	item, ok := s.keys[kid]
	if !ok || (item.alg != "" && item.alg != alg) {
		return nil, false
	}
	return item.key, true
}

func (s *GatewayResourceServer) refreshSigningKeys(ctx context.Context) error {
	jwksURL, err := s.resolveJWKSURL(ctx)
	if err != nil {
		return err
	}
	var set gatewayOIDCJWKSet
	if err := s.fetchJSON(ctx, jwksURL, &set); err != nil {
		return err
	}
	allowedAlgs := gatewayOIDCSet(splitGatewayOIDCList(s.cfg.AllowedSigningAlgs))
	keys := make(map[string]gatewayOIDCSigningKey, len(set.Keys))
	for _, jwk := range set.Keys {
		kid := strings.TrimSpace(jwk.Kid)
		if kid == "" || (jwk.Use != "" && jwk.Use != "sig") || !gatewayOIDCJWKAllowsVerify(jwk.KeyOps) {
			continue
		}
		if jwk.Alg != "" {
			if _, ok := allowedAlgs[jwk.Alg]; !ok {
				continue
			}
		}
		key, keyErr := gatewayOIDCParseJWK(jwk)
		if keyErr != nil {
			continue
		}
		if _, duplicate := keys[kid]; duplicate {
			return fmt.Errorf("duplicate jwks kid %q", kid)
		}
		keys[kid] = gatewayOIDCSigningKey{key: key, alg: strings.TrimSpace(jwk.Alg)}
	}
	if len(keys) == 0 {
		return errors.New("jwks contains no usable signing keys")
	}
	ttl := time.Duration(s.cfg.JWKSCacheTTLSeconds) * time.Second
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	s.mu.Lock()
	s.keys = keys
	s.expiresAt = time.Now().Add(ttl)
	s.jwksURL = jwksURL
	s.mu.Unlock()
	return nil
}

func (s *GatewayResourceServer) resolveJWKSURL(ctx context.Context) (string, error) {
	if configured := strings.TrimSpace(s.cfg.JWKSURL); configured != "" {
		return configured, gatewayOIDCValidateEndpointForIssuer(s.cfg.IssuerURL, configured)
	}
	s.mu.RLock()
	cached := s.jwksURL
	s.mu.RUnlock()
	if cached != "" {
		return cached, nil
	}
	discoveryURL := strings.TrimSpace(s.cfg.DiscoveryURL)
	if discoveryURL == "" {
		discoveryURL = strings.TrimRight(strings.TrimSpace(s.cfg.IssuerURL), "/") + "/.well-known/openid-configuration"
	}
	if err := gatewayOIDCValidateEndpointForIssuer(s.cfg.IssuerURL, discoveryURL); err != nil {
		return "", err
	}
	var doc gatewayOIDCDiscovery
	if err := s.fetchJSON(ctx, discoveryURL, &doc); err != nil {
		return "", err
	}
	if strings.TrimSpace(doc.Issuer) != strings.TrimSpace(s.cfg.IssuerURL) {
		return "", errors.New("oidc discovery issuer mismatch")
	}
	doc.JWKSURL = strings.TrimSpace(doc.JWKSURL)
	if err := gatewayOIDCValidateEndpointForIssuer(s.cfg.IssuerURL, doc.JWKSURL); err != nil {
		return "", err
	}
	return doc.JWKSURL, nil
}

func (s *GatewayResourceServer) fetchJSON(ctx context.Context, endpoint string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// net/http follows redirects by default. Revalidate the final URL so an
	// HTTPS issuer cannot redirect discovery/JWKS retrieval to cleartext HTTP.
	if resp.Request == nil || resp.Request.URL == nil {
		return errors.New("oidc metadata response is missing its final url")
	}
	if err := gatewayOIDCValidateEndpointForIssuer(s.cfg.IssuerURL, resp.Request.URL.String()); err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("oidc metadata request returned status %d", resp.StatusCode)
	}
	const maxMetadataBytes = 1 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMetadataBytes+1))
	if err != nil {
		return err
	}
	if len(body) > maxMetadataBytes {
		return errors.New("oidc metadata response exceeds size limit")
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return err
	}
	return nil
}

func (s *GatewayResourceServer) validateDelegation(claims jwt.MapClaims) ([]string, error) {
	actorClaim := strings.TrimSpace(s.cfg.TokenExchange.ActorClaim)
	actorValue, actorPresent := gatewayOIDCLookupClaim(claims, actorClaim)
	isExchange := gatewayOIDCIsTokenExchange(claims)
	if (s.cfg.TokenExchange.RequireActor || isExchange) && (!actorPresent || actorValue == nil) {
		return nil, ErrGatewayOIDCTokenInvalid
	}
	if !actorPresent || actorValue == nil {
		return nil, nil
	}
	allowedActors := gatewayOIDCSet(splitGatewayOIDCList(s.cfg.TokenExchange.AllowedActorClientIDs))
	// Delegation is deny-by-default. Merely presenting a well-formed `act`
	// object does not authorize an arbitrary actor.
	if len(allowedActors) == 0 {
		return nil, ErrGatewayOIDCTokenInvalid
	}
	maxDepth := s.cfg.TokenExchange.MaxDelegationDepth
	if maxDepth <= 0 {
		maxDepth = 4
	}
	chain := make([]string, 0, maxDepth)
	current := actorValue
	for depth := 0; current != nil; depth++ {
		if depth >= maxDepth {
			return nil, ErrGatewayOIDCTokenInvalid
		}
		actor, ok := current.(map[string]any)
		if !ok {
			return nil, ErrGatewayOIDCTokenInvalid
		}
		actorID, actorIDErr := gatewayOIDCActorID(actor)
		if actorIDErr != nil {
			return nil, ErrGatewayOIDCTokenInvalid
		}
		if len(allowedActors) > 0 {
			if _, ok := allowedActors[actorID]; !ok {
				return nil, ErrGatewayOIDCTokenInvalid
			}
		}
		chain = append(chain, actorID)
		current = actor["act"]
	}
	return chain, nil
}

func gatewayOIDCActorID(actor map[string]any) (string, error) {
	actorID := ""
	for _, claimName := range []string{"client_id", "azp", "sub"} {
		raw, present := actor[claimName]
		if !present {
			continue
		}
		value, ok := raw.(string)
		value = strings.TrimSpace(value)
		if !ok || value == "" {
			return "", ErrGatewayOIDCTokenInvalid
		}
		if actorID != "" && actorID != value {
			return "", ErrGatewayOIDCTokenInvalid
		}
		actorID = value
	}
	if actorID == "" {
		return "", ErrGatewayOIDCTokenInvalid
	}
	return actorID, nil
}

func gatewayOIDCClientID(claims jwt.MapClaims) (string, error) {
	resolved := ""
	for _, claimName := range []string{"azp", "client_id"} {
		raw, present := claims[claimName]
		if !present {
			continue
		}
		value, ok := raw.(string)
		value = strings.TrimSpace(value)
		if !ok || value == "" {
			return "", ErrGatewayOIDCTokenInvalid
		}
		if resolved != "" && resolved != value {
			return "", ErrGatewayOIDCTokenInvalid
		}
		resolved = value
	}
	if resolved == "" {
		return "", ErrGatewayOIDCTokenInvalid
	}
	return resolved, nil
}

func gatewayOIDCScopes(claims jwt.MapClaims) map[string]struct{} {
	result := make(map[string]struct{})
	for _, name := range []string{"scope", "scp"} {
		value, ok := claims[name]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case string:
			for _, item := range strings.Fields(typed) {
				result[item] = struct{}{}
			}
		case []any:
			for _, raw := range typed {
				if item, ok := raw.(string); ok && strings.TrimSpace(item) != "" {
					result[strings.TrimSpace(item)] = struct{}{}
				}
			}
		case []string:
			for _, item := range typed {
				if strings.TrimSpace(item) != "" {
					result[strings.TrimSpace(item)] = struct{}{}
				}
			}
		}
	}
	return result
}

func gatewayOIDCScopeSlice(scopes map[string]struct{}) []string {
	result := make([]string, 0, len(scopes))
	for scope := range scopes {
		result = append(result, scope)
	}
	return result
}

func gatewayOIDCIsTokenExchange(claims jwt.MapClaims) bool {
	for _, name := range []string{"gty", "grant_type"} {
		value, _ := gatewayOIDCStringClaim(claims, name)
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "urn:ietf:params:oauth:grant-type:token-exchange" || value == "token_exchange" || value == "token-exchange" {
			return true
		}
	}
	return false
}

func gatewayOIDCStringClaim(claims jwt.MapClaims, name string) (string, bool) {
	value, ok := gatewayOIDCLookupClaim(claims, name)
	if !ok {
		return "", false
	}
	text, ok := value.(string)
	return text, ok
}

func gatewayOIDCLookupClaim(claims jwt.MapClaims, name string) (any, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, false
	}
	if value, ok := claims[name]; ok {
		return value, true
	}
	parts := strings.Split(name, ".")
	var current any = map[string]any(claims)
	for _, part := range parts {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func gatewayOIDCValueAllowed(value string, allowed []string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(allowed) == 0 {
		return false
	}
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func gatewayOIDCSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			result[value] = struct{}{}
		}
	}
	return result
}

func splitGatewayOIDCList(raw string) []string {
	return strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func gatewayOIDCJWKAllowsVerify(ops []string) bool {
	if len(ops) == 0 {
		return true
	}
	for _, op := range ops {
		if op == "verify" {
			return true
		}
	}
	return false
}

func gatewayOIDCParseJWK(jwk gatewayOIDCJWK) (any, error) {
	switch strings.ToUpper(strings.TrimSpace(jwk.Kty)) {
	case "RSA":
		nBytes, err := base64.RawURLEncoding.DecodeString(jwk.N)
		if err != nil || len(nBytes) == 0 {
			return nil, errors.New("invalid rsa modulus")
		}
		modulus := new(big.Int).SetBytes(nBytes)
		if modulus.BitLen() < 2048 {
			return nil, errors.New("rsa modulus must be at least 2048 bits")
		}
		eBytes, err := base64.RawURLEncoding.DecodeString(jwk.E)
		if err != nil || len(eBytes) == 0 || len(eBytes) > 8 {
			return nil, errors.New("invalid rsa exponent")
		}
		exponent := 0
		for _, b := range eBytes {
			exponent = exponent<<8 + int(b)
		}
		if exponent < 3 {
			return nil, errors.New("invalid rsa exponent")
		}
		return &rsa.PublicKey{N: modulus, E: exponent}, nil
	case "EC":
		if jwk.Crv != "P-256" {
			return nil, errors.New("unsupported ec curve")
		}
		xBytes, err := base64.RawURLEncoding.DecodeString(jwk.X)
		if err != nil || len(xBytes) == 0 {
			return nil, errors.New("invalid ec x")
		}
		yBytes, err := base64.RawURLEncoding.DecodeString(jwk.Y)
		if err != nil || len(yBytes) == 0 {
			return nil, errors.New("invalid ec y")
		}
		key := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(xBytes), Y: new(big.Int).SetBytes(yBytes)}
		if !key.Curve.IsOnCurve(key.X, key.Y) {
			return nil, errors.New("ec point is not on curve")
		}
		return key, nil
	default:
		return nil, errors.New("unsupported jwk type")
	}
}

func gatewayOIDCValidateHTTPURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || strings.TrimSpace(parsed.Hostname()) == "" || (!strings.EqualFold(parsed.Scheme, "https") && !strings.EqualFold(parsed.Scheme, "http")) || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("invalid absolute http url")
	}
	return nil
}

func gatewayOIDCValidateEndpointForIssuer(issuer, endpoint string) error {
	if err := gatewayOIDCValidateHTTPURL(endpoint); err != nil {
		return err
	}
	issuerURL, issuerErr := url.Parse(strings.TrimSpace(issuer))
	endpointURL, endpointErr := url.Parse(strings.TrimSpace(endpoint))
	if issuerErr != nil || endpointErr != nil || issuerURL == nil || endpointURL == nil {
		return errors.New("invalid oidc issuer or endpoint url")
	}
	if strings.EqualFold(issuerURL.Scheme, "https") && !strings.EqualFold(endpointURL.Scheme, "https") {
		return errors.New("https oidc issuer cannot use an insecure metadata endpoint")
	}
	return nil
}
