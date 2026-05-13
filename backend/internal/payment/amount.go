package payment

import (
	"fmt"

	"github.com/shopspring/decimal"
)

const defaultMinorUnitsPerMajorUnit = 100

// DecimalAmountToMinorUnits converts a decimal amount string (e.g. "10.50")
// to minor units for currencies that use two decimal places.
// Uses shopspring/decimal for precision.
func DecimalAmountToMinorUnits(amountStr string) (int64, error) {
	d, err := decimal.NewFromString(amountStr)
	if err != nil {
		return 0, fmt.Errorf("invalid amount: %s", amountStr)
	}
	return d.Mul(decimal.NewFromInt(defaultMinorUnitsPerMajorUnit)).IntPart(), nil
}

// MinorUnitsToDecimal converts minor units to a float64 for interface compatibility.
func MinorUnitsToDecimal(amount int64) float64 {
	return decimal.NewFromInt(amount).Div(decimal.NewFromInt(defaultMinorUnitsPerMajorUnit)).InexactFloat64()
}

// YuanToFen converts a CNY yuan string (e.g. "10.50") to fen (int64).
// Uses shopspring/decimal for precision.
func YuanToFen(yuanStr string) (int64, error) {
	return DecimalAmountToMinorUnits(yuanStr)
}

// FenToYuan converts fen (int64) to yuan as a float64 for interface compatibility.
func FenToYuan(fen int64) float64 {
	return MinorUnitsToDecimal(fen)
}
