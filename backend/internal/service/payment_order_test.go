package service

import (
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
)

func TestBuildPaymentCallbackURLsForStripe(t *testing.T) {
	t.Parallel()

	notifyURL, returnURL, cancelURL := buildPaymentCallbackURLs(
		CreateOrderRequest{
			SrcURL:  "https://app.example.com/purchase",
			SrcHost: "ignored.example.com",
		},
		"stripe",
		"sub2_order_42",
		42,
	)

	if notifyURL != "https://app.example.com/api/v1/payment/webhook/stripe" {
		t.Fatalf("notifyURL = %q", notifyURL)
	}
	if returnURL != "https://app.example.com/payment/result?order_id=42&out_trade_no=sub2_order_42" {
		t.Fatalf("returnURL = %q", returnURL)
	}
	if cancelURL != "https://app.example.com/payment/result?order_id=42&out_trade_no=sub2_order_42&status=cancel" {
		t.Fatalf("cancelURL = %q", cancelURL)
	}
}

func TestBuildPaymentCallbackURLsFallsBackToHost(t *testing.T) {
	t.Parallel()

	notifyURL, returnURL, cancelURL := buildPaymentCallbackURLs(
		CreateOrderRequest{
			SrcHost: "pay.example.com",
		},
		"alipay",
		"sub2_order_99",
		99,
	)

	if notifyURL != "https://pay.example.com/api/v1/payment/webhook/alipay" {
		t.Fatalf("notifyURL = %q", notifyURL)
	}
	if returnURL != "https://pay.example.com/payment/result?order_id=99&out_trade_no=sub2_order_99" {
		t.Fatalf("returnURL = %q", returnURL)
	}
	if cancelURL != "https://pay.example.com/payment/result?order_id=99&out_trade_no=sub2_order_99&status=cancel" {
		t.Fatalf("cancelURL = %q", cancelURL)
	}
}

func TestGuessRequestOriginUsesHTTPForLocalhost(t *testing.T) {
	t.Parallel()

	got := guessRequestOrigin("localhost:5173")
	if got != "http://localhost:5173" {
		t.Fatalf("guessRequestOrigin() = %q", got)
	}
}

func TestBuildPaymentSubjectUsesTabroDefaults(t *testing.T) {
	t.Parallel()

	service := &PaymentService{}

	if got := service.buildPaymentSubject(nil, 100, &PaymentConfig{}, "CNY"); got != "Tabro 100.00 CNY" {
		t.Fatalf("top-up subject = %q", got)
	}

	plan := &dbent.SubscriptionPlan{Name: "Pro"}
	if got := service.buildPaymentSubject(plan, 0, &PaymentConfig{}, "USD"); got != "Tabro Subscription Pro" {
		t.Fatalf("subscription subject = %q", got)
	}
}
