package service

import (
	"context"
	"time"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
)

// GatewayBillingUsageIdentity is populated only from a verified gateway token.
// Local numeric user IDs and caller supplied tenant/app filters are not authority.
type GatewayBillingUsageIdentity struct {
	Issuer, Subject, Tenant, AppID string
}

type GatewayBillingUsageQuery struct {
	Identity              GatewayBillingUsageIdentity
	Start, End            time.Time // Half-open UTC range, after parsing the display timezone.
	Timezone, OperationID string
	Page, PageSize        int
}

type GatewayBillingUsageItem struct {
	ID                  string     `json:"id"`
	OperationID         string     `json:"operation_id"`
	RequestID           string     `json:"request_id"`
	Model               string     `json:"model"`
	ServiceTier         string     `json:"service_tier,omitempty"`
	InputTokens         int64      `json:"input_tokens"`
	OutputTokens        int64      `json:"output_tokens"`
	CacheCreationTokens int64      `json:"cache_creation_tokens"`
	CacheReadTokens     int64      `json:"cache_read_tokens"`
	ImageCount          int64      `json:"image_count"`
	MediaType           string     `json:"media_type,omitempty"`
	Credits             bc.Decimal `json:"credits"`
	RateMultiplier      bc.Decimal `json:"rate_multiplier"`
	State               string     `json:"state"`
	CreatedAt           time.Time  `json:"created_at"`
	SettledAt           *time.Time `json:"settled_at,omitempty"` // When API recorded Auth's payment confirmation.
}

type GatewayBillingUsageDay struct {
	Date         string `json:"date"`
	RequestCount int64  `json:"request_count"`
	// Credits is measured consumption, partitioned into settled, pending and
	// waived amounts below. A released operation cannot become a future debit.
	Credits        bc.Decimal `json:"credits"`
	SettledCredits bc.Decimal `json:"settled_credits"`
	PendingCredits bc.Decimal `json:"pending_credits"`
	WaivedCredits  bc.Decimal `json:"waived_credits"`
}

type GatewayBillingUsagePage struct {
	Unit     string                    `json:"unit"`
	Timezone string                    `json:"timezone"`
	Page     int                       `json:"page"`
	PageSize int                       `json:"page_size"`
	Total    int64                     `json:"total"`
	Items    []GatewayBillingUsageItem `json:"items"`
	Daily    []GatewayBillingUsageDay  `json:"daily"`
}

// GatewayBillingUsageRepository reads the durable ledger committed with billing.
// It must never return best-effort usage_logs as financial evidence.
type GatewayBillingUsageRepository interface {
	ListGatewayBillingUsage(context.Context, GatewayBillingUsageQuery) (*GatewayBillingUsagePage, error)
}
