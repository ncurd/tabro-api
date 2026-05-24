package service

import "testing"

func TestTotpIssuerIsTabro(t *testing.T) {
	t.Parallel()

	if totpIssuer != "Tabro" {
		t.Fatalf("totpIssuer = %q", totpIssuer)
	}
}
