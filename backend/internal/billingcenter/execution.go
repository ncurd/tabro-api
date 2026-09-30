package billingcenter

import (
	"context"
	"encoding/json"
	"sync"
)

type executionContextKey struct{}

// Execution contains no credentials. It follows the verified request to the
// supplier transport and usage task, including context.WithoutCancel copies.
type Execution struct {
	Key        Key
	Mode       string
	ProductKey string
	// Original meter bounds remain gateway evidence when Auth only sees a
	// pre-priced credit amount. They contain no bearer token or request body.
	OriginalMaximumUsage map[string]Decimal
	// GatewayPricingSnapshot freezes the price used for the admission bound.
	// It is opaque to Auth and contains neither credentials nor a request body.
	GatewayPricingSnapshot json.RawMessage
	RequestPayloadHash     string
	Quote                  Quote
	Coordinator            Coordinator
	mu                     sync.Mutex
	attempted              bool
	granted                bool
	handedOff              bool
}

func WithExecution(ctx context.Context, e *Execution) context.Context {
	return context.WithValue(ctx, executionContextKey{}, e)
}
func ExecutionFromContext(ctx context.Context) *Execution {
	if ctx == nil {
		return nil
	}
	e, _ := ctx.Value(executionContextKey{}).(*Execution)
	return e
}
func IsCentral(ctx context.Context) bool {
	e := ExecutionFromContext(ctx)
	return e != nil && e.Mode == "central"
}

// BeforeSupplierRequest must be called immediately before a supplier send.
// There is no retry permission after an ambiguous or rejected first attempt.
func BeforeSupplierRequest(ctx context.Context) error {
	e := ExecutionFromContext(ctx)
	if e == nil || e.Mode != "central" {
		return nil
	}
	if e.Coordinator.Store == nil || e.Coordinator.Authority == nil {
		return ErrState
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.attempted {
		return ErrState
	}
	e.attempted = true
	permit, err := e.Coordinator.Dispatch(ctx, e.Key)
	if err != nil {
		return err
	}
	if !permit {
		return ErrState
	}
	e.granted = true
	return nil
}

func (e *Execution) DispatchGranted() bool { e.mu.Lock(); defer e.mu.Unlock(); return e.granted }

type ExecutionSnapshot struct {
	Key                    Key                `json:"key"`
	Mode                   string             `json:"mode"`
	ProductKey             string             `json:"product_key"`
	OriginalMaximumUsage   map[string]Decimal `json:"original_maximum_usage,omitempty"`
	GatewayPricingSnapshot json.RawMessage    `json:"gateway_pricing_snapshot,omitempty"`
	RequestPayloadHash     string             `json:"request_payload_hash"`
	Quote                  Quote              `json:"quote"`
}

func (e *Execution) Snapshot() *ExecutionSnapshot {
	if e == nil {
		return nil
	}
	return &ExecutionSnapshot{Key: e.Key, Mode: e.Mode, ProductKey: e.ProductKey, OriginalMaximumUsage: e.OriginalMaximumUsage, GatewayPricingSnapshot: e.GatewayPricingSnapshot, RequestPayloadHash: e.RequestPayloadHash, Quote: e.Quote}
}
func (e *Execution) MarkHandedOff() {
	if e != nil {
		e.mu.Lock()
		e.handedOff = true
		e.mu.Unlock()
	}
}
func (e *Execution) HandedOff() bool { e.mu.Lock(); defer e.mu.Unlock(); return e.handedOff }
