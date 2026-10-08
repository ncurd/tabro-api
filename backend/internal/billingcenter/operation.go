// Package billingcenter contains the gateway's client and durable delivery
// contracts for Auth Billing. It never debits a local customer balance.
package billingcenter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrConflict             = errors.New("billing operation conflicts with persisted identity or content")
	ErrNotFound             = errors.New("billing operation not found")
	ErrState                = errors.New("billing operation state does not permit this transition")
	ErrLeaseLost            = errors.New("billing outbox lease is no longer owned")
	ErrCredentialsInPayload = errors.New("credentials cannot be persisted in billing payloads")
)

type Key struct {
	ProducerClientID string `json:"producer_client_id"`
	OriginAppID      string `json:"origin_app_id"`
	OperationID      string `json:"operation_id"`
}

func (k Key) Validate() error {
	for _, v := range []string{k.ProducerClientID, k.OriginAppID, k.OperationID} {
		if strings.TrimSpace(v) == "" || len(v) > 200 || strings.ContainsAny(v, "\r\n\x00") {
			return fmt.Errorf("invalid billing operation identity")
		}
	}
	return nil
}

type State string

const (
	Intent                 State = "intent"
	Reserved               State = "reserved"
	Dispatching            State = "dispatching"
	Dispatched             State = "dispatched"
	SettlementPending      State = "settlement_pending"
	ReleasePending         State = "release_pending"
	Settled                State = "settled"
	Released               State = "released"
	ReconciliationRequired State = "reconciliation_required"
)

// Operation is the durable local intent. No field may contain a bearer or
// subject token. The mode and owner epoch are frozen for the operation's life.
type Operation struct {
	Key
	ActorUserID        string
	TenantID           string
	RequestFingerprint string
	Mode               string
	OwnerEpoch         int64
	State              State
	Version            int64
	AttemptID          string
	ReservationID      string
	RemoteVersion      int64
	RequestPayload     json.RawMessage
	Snapshot           json.RawMessage
	// SettlementNotBefore freezes the daily billing cutoff at admission. Zero
	// preserves immediate settlement for operations created before scheduling.
	SettlementNotBefore time.Time
	// GatewayPricingSnapshot preserves the gateway's detailed prices and
	// multiplier for retries and asynchronous usage reconciliation.
	GatewayPricingSnapshot json.RawMessage
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

func (o Operation) ValidateIntent() error {
	if err := o.Key.Validate(); err != nil {
		return err
	}
	if o.ActorUserID == "" || o.TenantID == "" || o.OwnerEpoch < 0 {
		return ErrConflict
	}
	if o.Mode != "central" && o.Mode != "shadow" && o.Mode != "local" {
		return ErrConflict
	}
	if decoded, err := hex.DecodeString(o.RequestFingerprint); err != nil || len(decoded) != sha256.Size {
		return ErrConflict
	}
	return nil
}

// Fingerprint uses deterministic Go JSON encoding of typed data. Callers must
// pass the verified actor/tenant plus immutable request and price identity,
// excluding refreshable tokens and API key IDs. Monetary fields are strings.
func Fingerprint(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	if err := ValidatePersistentJSON(b); err != nil {
		return "", err
	}
	// Canonicalize nested RawMessage too, preserving numeric text with UseNumber.
	var canonical any
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.UseNumber()
	if err := decoder.Decode(&canonical); err != nil {
		return "", err
	}
	b, err = json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// ValidatePersistentJSON rejects credential fields recursively, including in
// nested evidence. Raw request headers/bodies must never become billing events.
func ValidatePersistentJSON(data []byte) error {
	var v any
	if !json.Valid(data) {
		return errors.New("invalid billing JSON")
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	var check func(any) error
	check = func(value any) error {
		switch x := value.(type) {
		case map[string]any:
			for key, child := range x {
				switch strings.ToLower(strings.ReplaceAll(key, "-", "_")) {
				case "subject_proof", "subject_token", "access_token", "refresh_token", "authorization", "client_secret", "api_key":
					return ErrCredentialsInPayload
				}
				if err := check(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range x {
				if err := check(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return check(v)
}

type EventKind string

const (
	SettleEvent  EventKind = "settle"
	ReleaseEvent EventKind = "release"
)

// Event is an immutable terminal command. Reserve/extend proofs are deliberately
// not supported by the persistent outbox. Retry preserves its body and event ID.
type Event struct {
	Key
	ID            string
	Kind          EventKind
	ReservationID string
	Body          json.RawMessage
	Fingerprint   string
	LeaseToken    string
	LeaseUntil    time.Time
	Attempts      int
}

func (e Event) Validate() error {
	if err := e.Key.Validate(); err != nil {
		return err
	}
	if e.ID == "" || len(e.ID) > 200 || e.ReservationID == "" {
		return ErrConflict
	}
	if e.Kind != SettleEvent && e.Kind != ReleaseEvent {
		return ErrState
	}
	if err := ValidatePersistentJSON(e.Body); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(e.Body))
	decoder.DisallowUnknownFields()
	var transition TransitionRequest
	if e.Kind == SettleEvent {
		var request SettleRequest
		if err := decoder.Decode(&request); err != nil {
			return err
		}
		if request.UsageItemID == "" || request.EvidenceReference == "" || len(request.Usage) == 0 {
			return ErrConflict
		}
		transition = request.TransitionRequest
	} else {
		var request ReleaseRequest
		if err := decoder.Decode(&request); err != nil {
			return err
		}
		if !request.ConfirmedNoBillableWork || request.Reason == "" {
			return ErrState
		}
		transition = request.TransitionRequest
	}
	if transition.EventID != e.ID || transition.ExpectedVersion <= 0 {
		return ErrConflict
	}
	return nil
}

type OutboxStore interface {
	Claim(context.Context, time.Duration, int) ([]Event, error)
	Complete(context.Context, Event, Reservation) error
	Retry(context.Context, Event, time.Duration, string, bool) error
}
