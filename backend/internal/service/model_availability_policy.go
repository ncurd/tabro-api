package service

import (
	"context"
	"errors"
)

// ModelAvailabilityPolicy is evaluated afresh for new invocations and catalogue
// reads. In-flight work keeps its existing admission and frozen price.
type ModelAvailabilityPolicy interface {
	IsModelEnabled(context.Context, string) (bool, error)
	FilterEnabledModels(context.Context, []string) ([]string, error)
}

var ErrModelDisabled = errors.New("model is disabled by the administrator")

type modelAvailabilityPolicyContextKey struct{}

func WithModelAvailabilityPolicy(ctx context.Context, policy ModelAvailabilityPolicy) context.Context {
	return context.WithValue(ctx, modelAvailabilityPolicyContextKey{}, policy)
}

func ModelAvailabilityPolicyFromContext(ctx context.Context) ModelAvailabilityPolicy {
	policy, _ := ctx.Value(modelAvailabilityPolicyContextKey{}).(ModelAvailabilityPolicy)
	return policy
}

func RequireModelEnabled(ctx context.Context, policy ModelAvailabilityPolicy, model string) error {
	if policy == nil || model == "" {
		return nil
	}
	enabled, err := policy.IsModelEnabled(ctx, model)
	if err != nil {
		return err
	}
	if !enabled {
		return ErrModelDisabled
	}
	return nil
}
