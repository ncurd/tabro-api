package service

import (
	"context"
	"fmt"
)

const (
	mediaUnitInputAudioSecond  = "input_audio_second"
	mediaUnitOutputAudioSecond = "output_audio_second"
	mediaUnitOutputVideoSecond = "output_video_second"
)

// Kept small so isolated billing fixtures can supply policy prices without an
// administrator HTTP handler, while production uses AdminModelPolicyService.
type mediaModelPolicyPricing interface {
	GetPricing(context.Context, string) (*ChannelModelPricing, error)
}

func (s *GatewayService) SetAdminModelPolicies(policies mediaModelPolicyPricing) {
	s.mediaModelPolicies = policies
}

func (s *MediaBillingService) SetAdminModelPolicies(policies mediaModelPolicyPricing) {
	s.modelPolicies = policies
}

// Both credit admission and local media billing must freeze the same price.
// An explicit channel price wins, including zero; an absent channel price can
// use the administrator's base price. Missing/invalid prices never become free.
func resolveMediaModelPrice(ctx context.Context, channelPrice *ChannelModelPricing, policies mediaModelPolicyPricing, model, kind string) (*MediaPriceSnapshot, error) {
	if channelPrice != nil {
		return snapshotMediaPrice(channelPrice, kind)
	}
	if policies == nil {
		return nil, ErrMediaPricingNotConfigured
	}
	price, err := policies.GetPricing(ctx, model)
	if err != nil {
		return nil, fmt.Errorf("%w: administrator model pricing is unavailable: %w", ErrMediaBillingUnavailable, err)
	}
	if price == nil {
		return nil, ErrMediaPricingNotConfigured
	}
	if price.BillingMode == BillingModeDuration {
		return snapshotMediaPrice(price, kind)
	}
	unit := ""
	switch {
	case price.BillingMode == BillingModePerRequest:
		unit = "request"
	case kind == MediaJobKindAudioTranscription || kind == MediaJobKindVideoGeneration:
		unit = "second"
	case kind == MediaJobKindAudioSpeech:
		unit = "character"
	case kind == MediaJobKindVoiceClone:
		unit = "request"
	}
	if unit == "" || price.PriceUnit != unit {
		return nil, fmt.Errorf("%w: administrator media price unit must be %s", ErrMediaPricingNotConfigured, unit)
	}
	return snapshotMediaPrice(price, kind)
}
