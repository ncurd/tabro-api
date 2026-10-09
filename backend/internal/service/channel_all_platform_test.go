//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

func TestChannelAllPlatformPricesAndMappingsUseTrustedEffectiveGroup(t *testing.T) {
	a, o := .1, .9
	channel := Channel{ID: 1, Status: StatusActive, GroupIDs: []int64{10}, RestrictModels: true,
		ModelPricing: []ChannelModelPricing{
			{Platform: PlatformAnthropic, Models: []string{"shared"}, InputPrice: &a},
			{Platform: PlatformOpenAI, Models: []string{"shared"}, InputPrice: &o},
		}, ModelMapping: map[string]map[string]string{
			PlatformAnthropic: {"shared": "claude-opus-5"},
			PlatformOpenAI:    {"shared": "gpt-6-sol"},
		}}
	s := newTestChannelService(makeStandardRepo(channel, map[int64]string{10: "all"}))
	for _, test := range []struct {
		platform, mapped string
		price            float64
	}{
		{PlatformAnthropic, "claude-opus-5", a}, {PlatformOpenAI, "gpt-6-sol", o},
	} {
		ctx := context.WithValue(context.Background(), ctxkey.Group, &Group{ID: 10, Platform: test.platform, Status: StatusActive, Hydrated: true})
		price := s.GetChannelModelPricing(ctx, 10, "shared")
		require.NotNil(t, price)
		require.Equal(t, test.price, *price.InputPrice)
		require.Equal(t, test.mapped, s.ResolveChannelMapping(ctx, 10, "shared").MappedModel)
		require.False(t, s.IsModelRestricted(ctx, 10, "shared"))
	}
	for _, group := range []*Group{nil,
		{ID: 10, Platform: PlatformOpenAI, Status: StatusActive},
		{ID: 11, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true},
	} {
		ctx := context.WithValue(context.Background(), ctxkey.Group, group)
		require.Nil(t, s.GetChannelModelPricing(ctx, 10, "shared"))
		require.Equal(t, "shared", s.ResolveChannelMapping(ctx, 10, "shared").MappedModel)
		require.True(t, s.IsModelRestricted(ctx, 10, "shared"))
	}
}
