package service

import "net/http"

// BillingRequestErrorCode distinguishes local admission failures from failures
// to contact the billing authority. Unknown authority and state errors keep
// their existing fail-closed behavior.
type BillingRequestErrorCode string

const (
	BillingRequestInvalid               BillingRequestErrorCode = "BILLING_REQUEST_INVALID"
	BillingRequestUnsupported           BillingRequestErrorCode = "BILLING_REQUEST_UNSUPPORTED"
	BillingModelCapabilitiesUnavailable BillingRequestErrorCode = "MODEL_CAPABILITIES_UNAVAILABLE"
	BillingModelPricingNotConfigured    BillingRequestErrorCode = "MODEL_PRICING_NOT_CONFIGURED"
)

// BillingRequestError contains only a safe, locally authored client message.
// Its optional cause remains available to errors.Is/As, but is deliberately
// excluded from Error so provider responses and credentials cannot escape.
type BillingRequestError struct {
	Code    BillingRequestErrorCode
	Message string
	cause   error
}

func NewBillingRequestError(code BillingRequestErrorCode, safeMessage string, cause ...error) *BillingRequestError {
	err := &BillingRequestError{Code: code, Message: safeMessage}
	if len(cause) > 0 {
		err.cause = cause[0]
	}
	return err
}

func (e *BillingRequestError) Error() string { return string(e.Code) + ": " + e.Message }
func (e *BillingRequestError) Unwrap() error { return e.cause }

func (e *BillingRequestError) HTTPStatus() int {
	switch e.Code {
	case BillingModelCapabilitiesUnavailable, BillingModelPricingNotConfigured:
		return http.StatusUnprocessableEntity
	default:
		return http.StatusBadRequest
	}
}
