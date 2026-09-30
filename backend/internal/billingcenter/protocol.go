package billingcenter

import (
	"encoding/json"
	"errors"
	"regexp"
	"time"
)

// Decimal is a base-ten wire value, never a JSON number or binary float.
type Decimal string

var decimalPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)

func (d Decimal) MarshalJSON() ([]byte, error) {
	if len(d) > 60 || !decimalPattern.MatchString(string(d)) {
		return nil, errors.New("billing quantities must be decimal strings")
	}
	return json.Marshal(string(d))
}
func (d *Decimal) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return errors.New("billing quantities must be decimal strings")
	}
	if len(value) > 60 || !decimalPattern.MatchString(value) {
		return errors.New("invalid billing decimal")
	}
	*d = Decimal(value)
	return nil
}

// ProofRequest is transient. Never persist or log this envelope.
type ProofRequest[T any] struct {
	OriginAppID         string `json:"origin_app_id"`
	SubjectProof        string `json:"subject_proof,omitempty"`
	CredentialBindingID string `json:"credential_binding_id,omitempty"`
	CredentialVersion   int64  `json:"credential_version,omitempty"`
	Request             T      `json:"request"`
}

func (p ProofRequest[T]) Validate() error {
	if p.OriginAppID == "" {
		return ErrConflict
	}
	if (p.SubjectProof != "") == (p.CredentialBindingID != "") {
		return errors.New("exactly one billing subject proof or credential binding is required")
	}
	if p.CredentialBindingID != "" && p.CredentialVersion <= 0 {
		return ErrConflict
	}
	return nil
}

type ReserveRequest struct {
	OperationID        string             `json:"operation_id"`
	BillingAccountID   string             `json:"billing_account_id"`
	BalanceID          string             `json:"balance_id"`
	PriceVersionID     string             `json:"price_version_id"`
	MaximumUsage       map[string]Decimal `json:"maximum_usage"`
	OwnerEpoch         int64              `json:"owner_epoch"`
	ServiceTier        string             `json:"service_tier"`
	MemberAllocationID string             `json:"member_allocation_id,omitempty"`
	ExpiresAt          *time.Time         `json:"expires_at,omitempty"`
	RequestPayloadHash string             `json:"request_payload_hash,omitempty"`
}
type TransitionRequest struct {
	EventID         string `json:"event_id"`
	ExpectedVersion int64  `json:"expected_version"`
}
type ExtendRequest struct {
	TransitionRequest
	MaximumUsage map[string]Decimal `json:"maximum_usage"`
}
type SettleRequest struct {
	TransitionRequest
	UsageItemID       string             `json:"usage_item_id"`
	Usage             map[string]Decimal `json:"usage"`
	EvidenceReference string             `json:"evidence_reference"`
	UsageComplete     bool               `json:"usage_complete"`
}
type ReleaseRequest struct {
	TransitionRequest
	Reason                  string `json:"reason"`
	ConfirmedNoBillableWork bool   `json:"confirmed_no_billable_work"`
}

func NewSettlement(key Key, reservationID string, request SettleRequest) (Event, error) {
	return newEvent(key, reservationID, SettleEvent, request.EventID, request)
}
func NewRelease(key Key, reservationID string, request ReleaseRequest) (Event, error) {
	if !request.ConfirmedNoBillableWork {
		return Event{}, ErrState
	}
	return newEvent(key, reservationID, ReleaseEvent, request.EventID, request)
}
func newEvent(key Key, reservationID string, kind EventKind, eventID string, request any) (Event, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return Event{}, err
	}
	event := Event{Key: key, ID: eventID, ReservationID: reservationID, Kind: kind, Body: body}
	if err := event.Validate(); err != nil {
		return Event{}, err
	}
	return event, nil
}
