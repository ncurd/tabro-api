package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func mediaScopeFixture(t *testing.T) (*APIKeyService, *APIKey, *gatewayRoutingGroupsStub, *mediaGenerationJobRepoStub, *MediaGenerationJob) {
	t.Helper()
	group := &Group{ID: 5, Platform: PlatformAll, Status: StatusActive, SubscriptionType: SubscriptionTypeStandard, Hydrated: true}
	groups := &gatewayRoutingGroupsStub{candidates: []GatewayRoutingCandidate{{Group: group}}}
	jobs := newMediaGenerationJobRepoStub()
	id := group.ID
	job := &MediaGenerationJob{PublicID: "voice_owned", Kind: MediaJobKindVoiceClone, Provider: MediaProviderDashScope, Platform: PlatformDashScope, UserID: 20, APIKeyID: 10, GroupID: &id, AccountID: 41, Status: MediaJobStatusSucceeded}
	require.NoError(t, jobs.Create(context.Background(), job))
	return &APIKeyService{groupRepo: groups, GatewayMediaResources: jobs}, routingTestKey(), groups, jobs, job
}

func TestGatewayMediaResourceChecksOwnerKeyAndRecordedScope(t *testing.T) {
	for _, mode := range []string{"public", "single-all", "foreign-user", "foreign-key", "wrong-kind", "private", "subscription", "disabled", "outside-single", "no-group"} {
		t.Run(mode, func(t *testing.T) {
			s, key, groups, jobs, job := mediaScopeFixture(t)
			kind := MediaJobKindVoiceClone
			wantAllowed := mode == "public" || mode == "single-all"
			switch mode {
			case "single-all":
				key.GroupScope, key.GroupID, key.Group = APIKeyGroupScopeSingle, job.GroupID, groups.candidates[0].Group
			case "foreign-user":
				job.UserID++
			case "foreign-key":
				job.APIKeyID++
			case "wrong-kind":
				kind = MediaJobKindVideoGeneration
			case "private":
				groups.candidates[0].Group.IsExclusive = true
			case "subscription":
				groups.candidates[0].Group.SubscriptionType = SubscriptionTypeSubscription
			case "disabled":
				groups.candidates[0].Group.Status = StatusDisabled
			case "outside-single":
				other := int64(8)
				key.GroupScope, key.GroupID, key.Group = APIKeyGroupScopeSingle, &other, groups.candidates[0].Group
			case "no-group":
				job.GroupID = nil
			}
			jobs.jobs[job.PublicID] = job
			resolved, _, err := s.ResolveGatewayMediaResource(context.Background(), key, job.PublicID, kind, "")
			if !wantAllowed {
				require.ErrorIs(t, err, ErrGatewayMediaResourceNotFound)
				return
			}
			require.NoError(t, err)
			require.Equal(t, int64(5), *resolved.GroupID)
			require.Equal(t, PlatformDashScope, resolved.Group.Platform)
			require.Equal(t, PlatformAll, groups.candidates[0].Group.Platform)
			require.NotSame(t, resolved, key)
		})
	}
}

func TestGatewayMediaResourceRechecksSelectedMembershipAndSubscription(t *testing.T) {
	s, key, groups, _, job := mediaScopeFixture(t)
	group := groups.candidates[0].Group
	group.Platform, group.IsExclusive = PlatformDashScope, true
	owner := &User{ID: 20, Status: StatusActive, AllowedGroups: []int64{group.ID}}
	s.userRepo = &gatewayRoutingOwnerStub{owner: owner}
	key.GroupScope, key.GroupIDs = APIKeyGroupScopeSelected, []int64{group.ID}
	_, _, err := s.ResolveGatewayMediaResource(context.Background(), key, job.PublicID, job.Kind, "")
	require.NoError(t, err)
	owner.AllowedGroups = nil
	_, _, err = s.ResolveGatewayMediaResource(context.Background(), key, job.PublicID, job.Kind, "")
	require.ErrorIs(t, err, ErrGatewayMediaResourceNotFound)
	group.IsExclusive, group.SubscriptionType = false, SubscriptionTypeSubscription
	_, _, err = s.ResolveGatewayMediaResource(context.Background(), key, job.PublicID, job.Kind, "")
	require.ErrorIs(t, err, ErrGatewayMediaResourceNotFound, "a selected subscription pool requires a current subscription")
	owner.Status = StatusDisabled
	_, _, err = s.ResolveGatewayMediaResource(context.Background(), key, job.PublicID, job.Kind, "")
	require.ErrorIs(t, err, ErrGroupNotAllowed)
}

func TestGatewayMediaResourceReadsWithoutAccountButSynthesisRequiresPinnedUsableAccount(t *testing.T) {
	s, key, groups, _, job := mediaScopeFixture(t)
	_, _, err := s.ResolveGatewayMediaResource(context.Background(), key, job.PublicID, job.Kind, "")
	require.NoError(t, err, "existing metadata is readable with no schedulable account")
	_, _, err = s.ResolveGatewayMediaResource(context.Background(), key, job.PublicID, job.Kind, QwenVoiceCloneModel)
	require.ErrorIs(t, err, ErrGatewayRouteUnavailable)
	account := Account{ID: 99, Platform: PlatformDashScope, Status: StatusActive, Schedulable: true, Type: AccountTypeAPIKey}
	groups.candidates[0].Accounts = []Account{account}
	_, _, err = s.ResolveGatewayMediaResource(context.Background(), key, job.PublicID, job.Kind, QwenVoiceCloneModel)
	require.ErrorIs(t, err, ErrGatewayRouteUnavailable, "another account must never replace the voice's credential")
	groups.candidates[0].Accounts[0].ID = job.AccountID
	_, _, err = s.ResolveGatewayMediaResource(context.Background(), key, job.PublicID, job.Kind, QwenVoiceCloneModel)
	require.NoError(t, err)
	groups.candidates[0].Accounts[0].Status = StatusDisabled
	_, _, err = s.ResolveGatewayMediaResource(context.Background(), key, job.PublicID, job.Kind, QwenVoiceCloneModel)
	require.ErrorIs(t, err, ErrGatewayRouteUnavailable)
	_, _, err = s.ResolveGatewayMediaResource(context.Background(), key, job.PublicID, job.Kind, "")
	require.NoError(t, err)
}

func TestGatewayMediaSynthesisRespectsAllChannelBillingModelRestrictions(t *testing.T) {
	for _, source := range []string{BillingModelSourceRequested, BillingModelSourceChannelMapped, BillingModelSourceUpstream} {
		for _, allowed := range []bool{true, false} {
			name := source + " denied"
			if allowed {
				name = source + " allowed"
			}
			t.Run(name, func(t *testing.T) {
				s, key, groups, _, job := mediaScopeFixture(t)
				groups.candidates[0].Accounts = []Account{{ID: job.AccountID, Platform: PlatformDashScope, Status: StatusActive, Schedulable: true, Type: AccountTypeAPIKey,
					Credentials: map[string]any{"model_mapping": map[string]any{"enrolled": "provider-model"}}}}
				priced := "forbidden-other-model"
				if allowed {
					switch source {
					case BillingModelSourceRequested:
						priced = "voice-alias"
					case BillingModelSourceChannelMapped:
						priced = "enrolled"
					case BillingModelSourceUpstream:
						priced = "provider-model"
					}
				}
				price := 0.1
				channel := Channel{ID: 1, Status: StatusActive, GroupIDs: []int64{5}, RestrictModels: true, BillingModelSource: source,
					ModelMapping: map[string]map[string]string{PlatformDashScope: {"voice-alias": "enrolled"}},
					ModelPricing: []ChannelModelPricing{{Platform: PlatformDashScope, Models: []string{priced}, InputPrice: &price}}}
				channels := &ChannelService{}
				channels.cache.Store(populateChannelCache([]Channel{channel}, map[int64]string{5: PlatformAll}))
				s.GatewayBilling = &GatewayBillingCoordinator{creditPricer: &gatewayCreditPriceCalculator{gateway: &GatewayService{channelService: channels}}}
				_, _, err := s.ResolveGatewayMediaResource(context.Background(), key, job.PublicID, job.Kind, "voice-alias")
				if allowed {
					require.NoError(t, err)
				} else {
					require.ErrorIs(t, err, ErrGatewayRouteUnavailable)
				}
			})
		}
	}
}

func TestGatewayMediaSynthesisCannotChangeTheEnrolledVoiceModel(t *testing.T) {
	s, key, groups, jobs, job := mediaScopeFixture(t)
	job.RequestJSON = []byte(`{"upstream_model":"qwen3-tts-vc-2026-01-22"}`)
	jobs.jobs[job.PublicID] = job
	groups.candidates[0].Accounts = []Account{{ID: job.AccountID, Platform: PlatformDashScope, Status: StatusActive, Schedulable: true, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"model_mapping": map[string]any{"different-model": "other-provider-model", QwenVoiceCloneModel: QwenVoiceCloneModel}}}}
	_, _, err := s.ResolveGatewayMediaResource(context.Background(), key, job.PublicID, job.Kind, "different-model")
	require.ErrorIs(t, err, ErrGatewayRouteUnavailable)
	_, _, err = s.ResolveGatewayMediaResource(context.Background(), key, job.PublicID, job.Kind, QwenVoiceCloneModel)
	require.NoError(t, err)
}

type mediaScopedListStub struct {
	MediaGenerationJobRepository
	jobs []*MediaGenerationJob
	ids  []int64
}

func (r *mediaScopedListStub) ListMediaResources(context.Context, int64, int64, *int64, string, int, int) ([]*MediaGenerationJob, error) {
	panic("must use authorized scope")
}
func (r *mediaScopedListStub) ListMediaResourcesByGroupIDs(_ context.Context, _, _ int64, ids []int64, _ string, _, _ int) ([]*MediaGenerationJob, error) {
	r.ids = ids
	return r.jobs, nil
}

func TestScopedClonedVoiceListPreservesOwnerAndCurrentGroupFilters(t *testing.T) {
	s, key, groups, _, job := mediaScopeFixture(t)
	outside := int64(99)
	foreign := *job
	foreign.UserID++
	wrongGroup := *job
	wrongGroup.GroupID = &outside
	list := &mediaScopedListStub{jobs: []*MediaGenerationJob{job, &foreign, &wrongGroup}}
	media := &MediaGenerationService{jobRepo: list, gatewayKeys: s}
	voices, err := media.ListClonedVoices(context.Background(), MediaRequestMeta{UserID: key.UserID, APIKeyID: key.ID, APIKey: key}, 20, 0)
	require.NoError(t, err)
	require.Equal(t, []int64{5}, list.ids)
	require.Len(t, voices, 1)
	groups.candidates[0].Group.IsExclusive = true
	voices, err = media.ListClonedVoices(context.Background(), MediaRequestMeta{UserID: key.UserID, APIKeyID: key.ID, APIKey: key}, 20, 0)
	require.NoError(t, err)
	require.Empty(t, list.ids)
	require.Empty(t, voices)
}
