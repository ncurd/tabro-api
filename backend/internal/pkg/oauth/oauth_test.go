package oauth

import (
	"net/url"
	"sync"
	"testing"
	"time"
)

func TestClaudeAuthorizationURLUsesCurrentEndpointAndPreservesPKCE(t *testing.T) {
	for _, scope := range []string{ScopeOAuth, ScopeInference} {
		t.Run(scope, func(t *testing.T) {
			parsed, err := url.Parse(BuildAuthorizationURL("state_-42", "challenge_-42", scope))
			if err != nil {
				t.Fatal(err)
			}
			if endpoint := parsed.Scheme + "://" + parsed.Host + parsed.Path; endpoint != "https://claude.com/cai/oauth/authorize" {
				t.Fatalf("unexpected authorization endpoint: %s", endpoint)
			}
			for key, want := range map[string]string{"code": "true", "client_id": ClientID, "response_type": "code", "redirect_uri": RedirectURI, "scope": scope, "code_challenge": "challenge_-42", "code_challenge_method": "S256", "state": "state_-42"} {
				if got := parsed.Query().Get(key); got != want {
					t.Errorf("%s: got %q, want %q", key, got, want)
				}
			}
		})
	}
}

func TestSessionStore_Stop_Idempotent(t *testing.T) {
	store := NewSessionStore()

	store.Stop()
	store.Stop()

	select {
	case <-store.stopCh:
		// ok
	case <-time.After(time.Second):
		t.Fatal("stopCh 未关闭")
	}
}

func TestSessionStore_Stop_Concurrent(t *testing.T) {
	store := NewSessionStore()

	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			store.Stop()
		}()
	}

	wg.Wait()

	select {
	case <-store.stopCh:
		// ok
	case <-time.After(time.Second):
		t.Fatal("stopCh 未关闭")
	}
}
