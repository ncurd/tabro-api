package service

import (
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/shopspring/decimal"
)

const defaultPaymentCurrency = "CNY"

var stripeBalanceCreditRates = map[string]float64{
	"CNY": 1,
	"USD": 6.5,
	"GBP": 9,
	"EUR": 7.6,
}

func normalizePaymentCurrency(paymentType, orderType, currency string) (string, error) {
	if payment.GetBasePaymentType(paymentType) != payment.TypeStripe || orderType != payment.OrderTypeBalance {
		return defaultPaymentCurrency, nil
	}

	normalized := strings.ToUpper(strings.TrimSpace(currency))
	if normalized == "" {
		normalized = defaultPaymentCurrency
	}
	if _, ok := stripeBalanceCreditRates[normalized]; !ok {
		return "", fmt.Errorf("unsupported currency: %s", normalized)
	}
	return normalized, nil
}

func calculateRechargeCredits(paymentAmount float64, paymentType, currency string, multiplier float64) float64 {
	if payment.GetBasePaymentType(paymentType) == payment.TypeStripe {
		rate := stripeBalanceCreditRates[strings.ToUpper(strings.TrimSpace(currency))]
		if rate <= 0 {
			rate = stripeBalanceCreditRates[defaultPaymentCurrency]
		}
		return decimal.NewFromFloat(paymentAmount).
			Mul(decimal.NewFromFloat(rate)).
			Round(2).
			InexactFloat64()
	}
	return calculateCreditedBalance(paymentAmount, multiplier)
}

func paymentTypeWithCurrency(paymentType, orderType, currency string) string {
	if payment.GetBasePaymentType(paymentType) != payment.TypeStripe || orderType != payment.OrderTypeBalance {
		return paymentType
	}
	normalized := strings.ToLower(strings.TrimSpace(currency))
	if normalized == "" {
		normalized = strings.ToLower(defaultPaymentCurrency)
	}
	return payment.TypeStripe + "_" + normalized
}
