package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

func TestNormalizePaymentCurrency(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		paymentType string
		orderType   string
		currency    string
		want        string
		wantErr     bool
	}{
		{name: "stripe balance defaults to CNY", paymentType: payment.TypeStripe, orderType: payment.OrderTypeBalance, want: "CNY"},
		{name: "stripe balance accepts USD", paymentType: payment.TypeStripe, orderType: payment.OrderTypeBalance, currency: "usd", want: "USD"},
		{name: "stripe balance accepts GBP", paymentType: payment.TypeStripe, orderType: payment.OrderTypeBalance, currency: "GBP", want: "GBP"},
		{name: "stripe balance accepts EUR", paymentType: payment.TypeStripe, orderType: payment.OrderTypeBalance, currency: "eur", want: "EUR"},
		{name: "non stripe always uses CNY", paymentType: payment.TypeAlipay, orderType: payment.OrderTypeBalance, currency: "USD", want: "CNY"},
		{name: "subscription always uses CNY", paymentType: payment.TypeStripe, orderType: payment.OrderTypeSubscription, currency: "USD", want: "CNY"},
		{name: "stripe rejects unsupported balance currency", paymentType: payment.TypeStripe, orderType: payment.OrderTypeBalance, currency: "JPY", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := normalizePaymentCurrency(tt.paymentType, tt.orderType, tt.currency)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestCalculateRechargeCreditsForCurrency(t *testing.T) {
	t.Parallel()

	require.InDelta(t, 50, calculateRechargeCredits(50, payment.TypeStripe, "CNY", 2), 1e-12)
	require.InDelta(t, 65, calculateRechargeCredits(10, payment.TypeStripe, "USD", 2), 1e-12)
	require.InDelta(t, 90, calculateRechargeCredits(10, payment.TypeStripe, "GBP", 2), 1e-12)
	require.InDelta(t, 76, calculateRechargeCredits(10, payment.TypeStripe, "EUR", 2), 1e-12)
	require.InDelta(t, 20, calculateRechargeCredits(10, payment.TypeAlipay, "CNY", 2), 1e-12)
}

func TestPaymentTypeWithCurrency(t *testing.T) {
	t.Parallel()

	require.Equal(t, "stripe_usd", paymentTypeWithCurrency(payment.TypeStripe, payment.OrderTypeBalance, "USD"))
	require.Equal(t, "stripe_cny", paymentTypeWithCurrency(payment.TypeStripe, payment.OrderTypeBalance, "CNY"))
	require.Equal(t, "stripe_eur", paymentTypeWithCurrency(payment.TypeStripe, payment.OrderTypeBalance, "EUR"))
	require.Equal(t, payment.TypeAlipay, paymentTypeWithCurrency(payment.TypeAlipay, payment.OrderTypeBalance, "USD"))
	require.Equal(t, payment.TypeStripe, paymentTypeWithCurrency(payment.TypeStripe, payment.OrderTypeSubscription, "GBP"))
}
