package config

import (
	"strings"
	"testing"
)

func TestDefaultCSPPolicyIncludesStripeDirectives(t *testing.T) {
	required := []string{
		"https://js.stripe.com",
		"https://*.js.stripe.com",
		"https://hooks.stripe.com",
		"https://api.stripe.com",
		"https://maps.googleapis.com",
	}

	for _, requiredDirective := range required {
		if !strings.Contains(DefaultCSPPolicy, requiredDirective) {
			t.Fatalf("DefaultCSPPolicy must include %s, got: %s", requiredDirective, DefaultCSPPolicy)
		}
	}
}
