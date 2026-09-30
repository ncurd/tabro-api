//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
)

func TestCentralPaymentProviderKeyRequiresOfficialMerchant(t *testing.T) {
	for _, tc := range []struct{ method, want string }{
		{payment.TypeAlipay, payment.TypeAlipay},
		{payment.TypeWxpay, payment.TypeWxpay},
		{payment.TypeStripe, ""},
		{payment.TypeEasyPay, ""},
	} {
		if got := centralPaymentProviderKey(tc.method); got != tc.want {
			t.Errorf("centralPaymentProviderKey(%q) = %q, want %q", tc.method, got, tc.want)
		}
	}
}

func TestGetWebhookProviderRejectsUnroutableOfficialCallbacks(t *testing.T) {
	s := &PaymentService{}
	for _, method := range []string{payment.TypeAlipay, payment.TypeWxpay} {
		if _, err := s.GetWebhookProvider(context.Background(), method, ""); err == nil {
			t.Errorf("%s callback with no clear-text order unexpectedly selected a merchant", method)
		}
	}
}

func TestCentralAlipayRequiresSellerIdentity(t *testing.T) {
	cases := []struct {
		name      string
		sel       *payment.InstanceSelection
		wantError bool
	}{
		{"matching direct merchant", &payment.InstanceSelection{ProviderKey: payment.TypeAlipay, Config: map[string]string{"sellerId": "2088..."}}, false},
		{"missing seller ID", &payment.InstanceSelection{ProviderKey: payment.TypeAlipay, Config: map[string]string{}}, true},
		{"blank seller ID", &payment.InstanceSelection{ProviderKey: payment.TypeAlipay, Config: map[string]string{"sellerId": "  "}}, true},
		{"aggregate provider", &payment.InstanceSelection{ProviderKey: payment.TypeEasyPay, Config: map[string]string{"sellerId": "2088..."}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCentralMerchantSelection(payment.TypeAlipay, payment.TypeAlipay, tc.sel)
			if (err != nil) != tc.wantError {
				t.Fatalf("validateCentralMerchantSelection error = %v, wantError = %v", err, tc.wantError)
			}
		})
	}
}
