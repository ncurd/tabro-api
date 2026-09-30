package billingcenter

import "testing"

func TestCentralPaymentAmountsStayExactAndRejectSubcentOrUnknownAmounts(t *testing.T) {
	if !PaymentAmountsEqual("90071992547409.91", "90071992547409.9100") {
		t.Fatal("decimal amount lost precision")
	}
	for _, invalid := range []Decimal{"", "0", "-1", "1.001", "1e2", "92233720368547758.08"} {
		if _, err := PaymentMinorUnits(invalid); err == nil {
			t.Fatalf("accepted invalid payment %q", invalid)
		}
	}
	amount, err := PaymentMinorUnits("90071992547409.91")
	if err != nil || amount != 9007199254740991 {
		t.Fatalf("unexpected exact minor units %d: %v", amount, err)
	}
}
