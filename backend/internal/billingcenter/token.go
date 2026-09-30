package billingcenter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type TokenConfig struct {
	TokenURL      string
	ClientID      string
	ClientSecret  string
	Scopes        []string
	Timeout       time.Duration
	InsecureLocal bool
}

type ClientCredentialsTokenSource struct {
	config  TokenConfig
	http    *http.Client
	gate    chan struct{}
	mu      sync.Mutex
	token   string
	expires time.Time
}

func NewClientCredentialsTokenSource(cfg TokenConfig, transport http.RoundTripper) (*ClientCredentialsTokenSource, error) {
	if _, err := validatedURL(cfg.TokenURL, cfg.InsecureLocal); err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.ClientID) == "" || strings.TrimSpace(cfg.ClientSecret) == "" || len(cfg.Scopes) == 0 {
		return nil, errors.New("billing OAuth client credentials and scopes are required")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &ClientCredentialsTokenSource{config: cfg, gate: make(chan struct{}, 1), http: &http.Client{Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (s *ClientCredentialsTokenSource) Invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = ""
	s.expires = time.Time{}
}

func (s *ClientCredentialsTokenSource) Token(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, s.config.Timeout)
	defer cancel()
	select {
	case s.gate <- struct{}{}:
		defer func() { <-s.gate }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	s.mu.Lock()
	token, expires := s.token, s.expires
	s.mu.Unlock()
	if token != "" && time.Now().Before(expires) {
		return token, nil
	}
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {s.config.ClientID}, "client_secret": {s.config.ClientSecret}, "scope": {strings.Join(s.config.Scopes, " ")}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.config.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", errors.New("invalid billing token request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := s.http.Do(req)
	if err != nil {
		return "", errors.New("billing token request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", errors.New("billing OAuth credentials rejected")
	}
	var result struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 65536)).Decode(&result); err != nil || result.AccessToken == "" || !strings.EqualFold(result.TokenType, "Bearer") || result.ExpiresIn <= 0 || result.ExpiresIn > 86400 {
		return "", errors.New("invalid billing token response")
	}
	lifetime := time.Duration(result.ExpiresIn) * time.Second
	skew := 30 * time.Second
	if lifetime/10 < skew {
		skew = lifetime / 10
	}
	s.mu.Lock()
	s.token = result.AccessToken
	s.expires = time.Now().Add(lifetime - skew)
	s.mu.Unlock()
	return result.AccessToken, nil
}
