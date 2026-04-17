package service

import "testing"

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
