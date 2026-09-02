package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type openAIFastPolicyRepoStub struct {
	values        map[string]string
	getValueCalls int
}

func (s *openAIFastPolicyRepoStub) Get(ctx context.Context, key string) (*Setting, error) {
	panic("unexpected Get call")
}

func (s *openAIFastPolicyRepoStub) GetValue(ctx context.Context, key string) (string, error) {
	s.getValueCalls++
	if v, ok := s.values[key]; ok {
		return v, nil
	}
	return "", ErrSettingNotFound
}

func (s *openAIFastPolicyRepoStub) Set(ctx context.Context, key, value string) error {
	if s.values == nil {
		s.values = map[string]string{}
	}
	s.values[key] = value
	return nil
}

func (s *openAIFastPolicyRepoStub) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := s.values[key]; ok {
			result[key] = value
		}
	}
	return result, nil
}

func (s *openAIFastPolicyRepoStub) SetMultiple(ctx context.Context, settings map[string]string) error {
	panic("unexpected SetMultiple call")
}

func (s *openAIFastPolicyRepoStub) GetAll(ctx context.Context) (map[string]string, error) {
	panic("unexpected GetAll call")
}

func (s *openAIFastPolicyRepoStub) Delete(ctx context.Context, key string) error {
	panic("unexpected Delete call")
}

type blockingOpenAIFastPolicyRepo struct {
	*openAIFastPolicyRepoStub
	mu          sync.Mutex
	value       string
	readStarted chan struct{}
	releaseRead chan struct{}
	startOnce   sync.Once
}

type observedDoneContext struct {
	context.Context
	doneObserved chan struct{}
	observeOnce  sync.Once
}

func (c *observedDoneContext) Done() <-chan struct{} {
	c.observeOnce.Do(func() { close(c.doneObserved) })
	return c.Context.Done()
}

func (s *blockingOpenAIFastPolicyRepo) GetValue(ctx context.Context, key string) (string, error) {
	s.mu.Lock()
	value := s.value
	s.mu.Unlock()
	s.startOnce.Do(func() { close(s.readStarted) })
	select {
	case <-s.releaseRead:
		return value, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (s *blockingOpenAIFastPolicyRepo) Set(ctx context.Context, key, value string) error {
	s.mu.Lock()
	s.value = value
	s.mu.Unlock()
	return nil
}

func newOpenAIGatewayServiceWithSettings(t *testing.T, settings *OpenAIFastPolicySettings) *OpenAIGatewayService {
	t.Helper()
	repo := &openAIFastPolicyRepoStub{values: map[string]string{}}
	if settings != nil {
		raw, err := json.Marshal(settings)
		require.NoError(t, err)
		repo.values[SettingKeyOpenAIFastPolicySettings] = string(raw)
	}
	return &OpenAIGatewayService{
		settingService: NewSettingService(repo, &config.Config{}),
	}
}

func openAIFastFilterPriorityPolicy() *OpenAIFastPolicySettings {
	return &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier:    OpenAIFastTierPriority,
			Action:         BetaPolicyActionFilter,
			Scope:          BetaPolicyScopeAll,
			ModelWhitelist: []string{},
			FallbackAction: BetaPolicyActionPass,
		}},
	}
}

func TestEvaluateOpenAIFastPolicy_DefaultPassesKnownTiers(t *testing.T) {
	require.Empty(t, DefaultOpenAIFastPolicySettings().Rules, "default policy must not rewrite service_tier unless admin configured rules")

	svc := newOpenAIGatewayServiceWithSettings(t, DefaultOpenAIFastPolicySettings())
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	action, _ := svc.evaluateOpenAIFastPolicy(context.Background(), account, "gpt-5.5", OpenAIFastTierPriority)
	require.Equal(t, BetaPolicyActionPass, action)

	action, _ = svc.evaluateOpenAIFastPolicy(context.Background(), account, "gpt-5.5-turbo", OpenAIFastTierPriority)
	require.Equal(t, BetaPolicyActionPass, action)

	action, _ = svc.evaluateOpenAIFastPolicy(context.Background(), account, "gpt-4", OpenAIFastTierPriority)
	require.Equal(t, BetaPolicyActionPass, action)

	action, _ = svc.evaluateOpenAIFastPolicy(context.Background(), account, "gpt-5.5", OpenAIFastTierFlex)
	require.Equal(t, BetaPolicyActionPass, action)

	// empty tier → pass
	action, _ = svc.evaluateOpenAIFastPolicy(context.Background(), account, "gpt-5.5", "")
	require.Equal(t, BetaPolicyActionPass, action)
}

func TestEvaluateOpenAIFastPolicy_BlockRuleCarriesMessage(t *testing.T) {
	settings := &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier:    OpenAIFastTierPriority,
			Action:         BetaPolicyActionBlock,
			Scope:          BetaPolicyScopeAll,
			ErrorMessage:   "fast mode is not allowed",
			ModelWhitelist: []string{"gpt-5.5"},
			FallbackAction: BetaPolicyActionPass,
		}},
	}
	svc := newOpenAIGatewayServiceWithSettings(t, settings)
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	action, msg := svc.evaluateOpenAIFastPolicy(context.Background(), account, "gpt-5.5", OpenAIFastTierPriority)
	require.Equal(t, BetaPolicyActionBlock, action)
	require.Equal(t, "fast mode is not allowed", msg)
}

func TestEvaluateOpenAIFastPolicy_ScopeFiltersOAuth(t *testing.T) {
	settings := &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier: OpenAIFastTierAny,
			Action:      BetaPolicyActionFilter,
			Scope:       BetaPolicyScopeOAuth,
		}},
	}
	svc := newOpenAIGatewayServiceWithSettings(t, settings)

	// OAuth account → rule matches
	oauthAccount := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	action, _ := svc.evaluateOpenAIFastPolicy(context.Background(), oauthAccount, "gpt-4", OpenAIFastTierPriority)
	require.Equal(t, BetaPolicyActionFilter, action)

	// API Key account → rule skipped → pass
	apiKeyAccount := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	action, _ = svc.evaluateOpenAIFastPolicy(context.Background(), apiKeyAccount, "gpt-4", OpenAIFastTierPriority)
	require.Equal(t, BetaPolicyActionPass, action)
}

func TestEvaluateOpenAIFastPolicy_UserScopedRuleOverridesGlobalRule(t *testing.T) {
	settings := &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{
			{
				ServiceTier: OpenAIFastTierPriority,
				Action:      BetaPolicyActionFilter,
				Scope:       BetaPolicyScopeAll,
			},
			{
				ServiceTier: OpenAIFastTierPriority,
				Action:      BetaPolicyActionPass,
				Scope:       BetaPolicyScopeAll,
				UserIDs:     []int64{42},
			},
		},
	}
	svc := newOpenAIGatewayServiceWithSettings(t, settings)
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	allowedUserCtx := context.WithValue(context.Background(), ctxkey.UserID, int64(42))
	action, _ := svc.evaluateOpenAIFastPolicy(allowedUserCtx, account, "gpt-5.5", OpenAIFastTierPriority)
	require.Equal(t, BetaPolicyActionPass, action)

	otherUserCtx := context.WithValue(context.Background(), ctxkey.UserID, int64(43))
	action, _ = svc.evaluateOpenAIFastPolicy(otherUserCtx, account, "gpt-5.5", OpenAIFastTierPriority)
	require.Equal(t, BetaPolicyActionFilter, action)

	action, _ = svc.evaluateOpenAIFastPolicy(context.Background(), account, "gpt-5.5", OpenAIFastTierPriority)
	require.Equal(t, BetaPolicyActionFilter, action)
}

func TestApplyOpenAIFastPolicyToBody_DefaultPassesPriorityAndFast(t *testing.T) {
	svc := newOpenAIGatewayServiceWithSettings(t, DefaultOpenAIFastPolicySettings())
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	body := []byte(`{"model":"gpt-5.5","service_tier":"priority","messages":[]}`)
	updated, err := svc.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-5.5", body)
	require.NoError(t, err)
	require.Equal(t, string(body), string(updated))

	body = []byte(`{"model":"gpt-5.5","service_tier":"fast"}`)
	updated, err = svc.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-5.5", body)
	require.NoError(t, err)
	require.Equal(t, "priority", gjson.GetBytes(updated, "service_tier").String())

	body = []byte(`{"model":"gpt-4","service_tier":"priority"}`)
	updated, err = svc.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-4", body)
	require.NoError(t, err)
	require.Equal(t, string(body), string(updated))

	// No service_tier → no-op
	body = []byte(`{"model":"gpt-5.5"}`)
	updated, err = svc.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-5.5", body)
	require.NoError(t, err)
	require.Equal(t, string(body), string(updated))
}

func TestApplyOpenAIFastPolicyToBody_ExplicitFilterRemovesField(t *testing.T) {
	svc := newOpenAIGatewayServiceWithSettings(t, openAIFastFilterPriorityPolicy())
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	body := []byte(`{"model":"gpt-5.5","service_tier":"priority","messages":[]}`)
	updated, err := svc.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-5.5", body)
	require.NoError(t, err)
	require.NotContains(t, string(updated), `"service_tier"`)

	body = []byte(`{"model":"gpt-5.5","service_tier":"fast"}`)
	updated, err = svc.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-5.5", body)
	require.NoError(t, err)
	require.NotContains(t, string(updated), `"service_tier"`)
}

func TestApplyOpenAIFastPolicyToBody_UserScopedRuleOverridesGlobalRule(t *testing.T) {
	settings := &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{
			{
				ServiceTier: OpenAIFastTierPriority,
				Action:      BetaPolicyActionFilter,
				Scope:       BetaPolicyScopeAll,
			},
			{
				ServiceTier: OpenAIFastTierPriority,
				Action:      BetaPolicyActionPass,
				Scope:       BetaPolicyScopeAll,
				UserIDs:     []int64{42},
			},
		},
	}
	svc := newOpenAIGatewayServiceWithSettings(t, settings)
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	body := []byte(`{"model":"gpt-5.5","service_tier":"priority"}`)

	allowedUserCtx := context.WithValue(context.Background(), ctxkey.UserID, int64(42))
	updated, err := svc.applyOpenAIFastPolicyToBody(allowedUserCtx, account, "gpt-5.5", body)
	require.NoError(t, err)
	require.Equal(t, "priority", gjson.GetBytes(updated, "service_tier").String())

	otherUserCtx := context.WithValue(context.Background(), ctxkey.UserID, int64(43))
	updated, err = svc.applyOpenAIFastPolicyToBody(otherUserCtx, account, "gpt-5.5", body)
	require.NoError(t, err)
	require.NotContains(t, string(updated), `"service_tier"`)
}

func TestApplyOpenAIFastPolicyToBody_ForcePriorityRewritesKnownTier(t *testing.T) {
	settings := &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier: OpenAIFastTierAny,
			Action:      OpenAIFastPolicyActionForcePriority,
			Scope:       BetaPolicyScopeAll,
		}},
	}
	svc := newOpenAIGatewayServiceWithSettings(t, settings)
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	for _, tier := range []string{"flex", "auto", "default", "scale", "fast", "priority"} {
		body := []byte(`{"model":"gpt-5.5","service_tier":"` + tier + `"}`)
		updated, err := svc.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-5.5", body)
		require.NoError(t, err)
		require.Equal(t, OpenAIFastTierPriority, gjson.GetBytes(updated, "service_tier").String(),
			"tier %q should be forced to priority", tier)
	}
}

func TestApplyOpenAIFastPolicyToBody_ForcePriorityMissingTierRequiresExplicitInjection(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	body := []byte(`{"model":"gpt-5.5","input":[]}`)

	withoutInjection := newOpenAIGatewayServiceWithSettings(t, &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier: OpenAIFastTierAny,
			Action:      OpenAIFastPolicyActionForcePriority,
			Scope:       BetaPolicyScopeAll,
		}},
	})
	updated, err := withoutInjection.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-5.5", body)
	require.NoError(t, err)
	require.Equal(t, string(body), string(updated))

	withInjection := newOpenAIGatewayServiceWithSettings(t, &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier:             OpenAIFastTierAny,
			Action:                  OpenAIFastPolicyActionForcePriority,
			Scope:                   BetaPolicyScopeAll,
			InjectPriorityIfMissing: true,
		}},
	})
	updated, err = withInjection.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-5.5", body)
	require.NoError(t, err)
	require.Equal(t, OpenAIFastTierPriority, gjson.GetBytes(updated, "service_tier").String())
}

func TestApplyOpenAIFastPolicyToBody_ForcePriorityDoesNotInjectOverExplicitInvalidTier(t *testing.T) {
	svc := newOpenAIGatewayServiceWithSettings(t, &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier:             OpenAIFastTierAny,
			Action:                  OpenAIFastPolicyActionForcePriority,
			Scope:                   BetaPolicyScopeAll,
			InjectPriorityIfMissing: true,
		}},
	})
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	for _, body := range [][]byte{
		[]byte(`{"model":"gpt-5.5","service_tier":null}`),
		[]byte(`{"model":"gpt-5.5","service_tier":""}`),
		[]byte(`{"model":"gpt-5.5","service_tier":"unknown"}`),
	} {
		updated, err := svc.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-5.5", body)
		require.NoError(t, err)
		require.Equal(t, string(body), string(updated))
	}
}

func TestApplyOpenAIFastPolicyToNormalizedBody_DoesNotInjectFieldRemovedByNormalizer(t *testing.T) {
	svc := newOpenAIGatewayServiceWithSettings(t, &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier:             OpenAIFastTierAny,
			Action:                  OpenAIFastPolicyActionForcePriority,
			Scope:                   BetaPolicyScopeAll,
			InjectPriorityIfMissing: true,
		}},
	})
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	normalizedBody := []byte(`{"model":"gpt-5.5","input":[]}`)

	updated, err := svc.applyOpenAIFastPolicyToNormalizedBody(
		context.Background(),
		account,
		"gpt-5.5",
		normalizedBody,
		true,
	)
	require.NoError(t, err)
	require.Equal(t, string(normalizedBody), string(updated))

	updated, err = svc.applyOpenAIFastPolicyToNormalizedBody(
		context.Background(),
		account,
		"gpt-5.5",
		normalizedBody,
		false,
	)
	require.NoError(t, err)
	require.Equal(t, OpenAIFastTierPriority, gjson.GetBytes(updated, "service_tier").String())
}

func TestApplyOpenAIFastPolicyToBody_MissingTierInjectionIsOpenAIOnly(t *testing.T) {
	svc := newOpenAIGatewayServiceWithSettings(t, &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier:             OpenAIFastTierAny,
			Action:                  OpenAIFastPolicyActionForcePriority,
			Scope:                   BetaPolicyScopeAll,
			InjectPriorityIfMissing: true,
		}},
	})
	body := []byte(`{"model":"gpt-5.5","input":[]}`)

	for _, platform := range []string{PlatformAnthropic, PlatformGemini, PlatformGrok} {
		updated, err := svc.applyOpenAIFastPolicyToBody(
			context.Background(),
			&Account{Platform: platform, Type: AccountTypeAPIKey},
			"gpt-5.5",
			body,
		)
		require.NoError(t, err)
		require.Equal(t, string(body), string(updated), "platform %q must not receive OpenAI service_tier", platform)
	}

	compatibleAccount := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://api.deepseek.com/v1",
		},
	}
	updated, err := svc.applyOpenAIFastPolicyToBody(
		context.Background(),
		compatibleAccount,
		"gpt-5.5",
		body,
	)
	require.NoError(t, err)
	require.Equal(t, string(body), string(updated),
		"third-party OpenAI-compatible upstreams must not receive service_tier=priority")

	officialAccount := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://API.OPENAI.COM./v1",
		},
	}
	updated, err = svc.applyOpenAIFastPolicyToBody(
		context.Background(),
		officialAccount,
		"gpt-5.5",
		body,
	)
	require.NoError(t, err)
	require.Equal(t, OpenAIFastTierPriority, gjson.GetBytes(updated, "service_tier").String())
}

func TestApplyOpenAIFastPolicyToBody_MissingTierInjectionHonorsOAuthScope(t *testing.T) {
	svc := newOpenAIGatewayServiceWithSettings(t, &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier:             OpenAIFastTierAny,
			Action:                  OpenAIFastPolicyActionForcePriority,
			Scope:                   BetaPolicyScopeOAuth,
			InjectPriorityIfMissing: true,
		}},
	})
	body := []byte(`{"model":"gpt-5.5","input":[]}`)

	oauthBody, err := svc.applyOpenAIFastPolicyToBody(
		context.Background(),
		&Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth},
		"gpt-5.5",
		body,
	)
	require.NoError(t, err)
	require.Equal(t, OpenAIFastTierPriority, gjson.GetBytes(oauthBody, "service_tier").String())

	apiKeyBody, err := svc.applyOpenAIFastPolicyToBody(
		context.Background(),
		&Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
		"gpt-5.5",
		body,
	)
	require.NoError(t, err)
	require.Equal(t, string(body), string(apiKeyBody))
}

func TestApplyOpenAIFastPolicyToBody_RuntimeRejectsMalformedMissingTierRule(t *testing.T) {
	svc := newOpenAIGatewayServiceWithSettings(t, &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier:             "",
			Action:                  OpenAIFastPolicyActionForcePriority,
			Scope:                   BetaPolicyScopeAll,
			InjectPriorityIfMissing: true,
		}},
	})
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	body := []byte(`{"model":"gpt-5.5","input":[]}`)

	updated, err := svc.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-5.5", body)
	require.NoError(t, err)
	require.Equal(t, string(body), string(updated))
}

func TestApplyOpenAIFastPolicyToBody_MissingTierKeepsFirstMatchOrderAndWhitelist(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	body := []byte(`{"model":"gpt-5.5","input":[]}`)

	firstPassWins := newOpenAIGatewayServiceWithSettings(t, &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{
			{
				ServiceTier: OpenAIFastTierAny,
				Action:      BetaPolicyActionPass,
				Scope:       BetaPolicyScopeAll,
			},
			{
				ServiceTier:             OpenAIFastTierAny,
				Action:                  OpenAIFastPolicyActionForcePriority,
				Scope:                   BetaPolicyScopeAll,
				InjectPriorityIfMissing: true,
			},
		},
	})
	updated, err := firstPassWins.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-5.5", body)
	require.NoError(t, err)
	require.Equal(t, string(body), string(updated))

	whitelisted := newOpenAIGatewayServiceWithSettings(t, &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier:             OpenAIFastTierAny,
			Action:                  OpenAIFastPolicyActionForcePriority,
			Scope:                   BetaPolicyScopeAll,
			InjectPriorityIfMissing: true,
			ModelWhitelist:          []string{"gpt-5.5"},
			FallbackAction:          BetaPolicyActionPass,
		}},
	})
	updated, err = whitelisted.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-5.5", body)
	require.NoError(t, err)
	require.Equal(t, OpenAIFastTierPriority, gjson.GetBytes(updated, "service_tier").String())

	updated, err = whitelisted.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-5.4", body)
	require.NoError(t, err)
	require.Equal(t, string(body), string(updated))
}

func TestApplyOpenAIFastPolicyToBody_GroupForceInjectsAndOverridesTier(t *testing.T) {
	svc := newOpenAIGatewayServiceWithSettings(t, DefaultOpenAIFastPolicySettings())
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	ctx := context.WithValue(context.Background(), ctxkey.Group, &Group{
		ID: 7, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true, ForceOpenAIFast: true,
	})

	for _, body := range [][]byte{
		[]byte(`{"model":"gpt-5.6-sol","input":"hi"}`),
		[]byte(`{"model":"gpt-5.6-sol","service_tier":"default"}`),
		[]byte(`{"model":"gpt-5.6-sol","service_tier":"flex"}`),
		[]byte(`{"model":"gpt-5.6-sol","service_tier":"client-unknown"}`),
	} {
		updated, err := svc.applyOpenAIFastPolicyToBody(ctx, account, "gpt-5.6-sol", body)
		require.NoError(t, err)
		require.Equal(t, OpenAIFastTierPriority, gjson.GetBytes(updated, "service_tier").String())
	}
}

func TestApplyOpenAIFastPolicyToBody_GroupForceStillHonorsGlobalPolicy(t *testing.T) {
	svc := newOpenAIGatewayServiceWithSettings(t, openAIFastFilterPriorityPolicy())
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	ctx := context.WithValue(context.Background(), ctxkey.Group, &Group{
		ID: 7, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true, ForceOpenAIFast: true,
	})

	updated, err := svc.applyOpenAIFastPolicyToBody(ctx, account, "gpt-5.6-sol", []byte(`{"model":"gpt-5.6-sol"}`))
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(updated, "service_tier").Exists(),
		"the global filter remains authoritative after the group requests priority")
}

func TestApplyOpenAIFastPolicyToBody_GroupForceRequiresHydratedGroup(t *testing.T) {
	svc := newOpenAIGatewayServiceWithSettings(t, DefaultOpenAIFastPolicySettings())
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	body := []byte(`{"model":"gpt-5.6-sol"}`)
	ctx := context.WithValue(context.Background(), ctxkey.Group, &Group{
		ID: 7, Platform: PlatformOpenAI, Status: StatusActive, ForceOpenAIFast: true,
	})

	updated, err := svc.applyOpenAIFastPolicyToBody(ctx, account, "gpt-5.6-sol", body)
	require.NoError(t, err)
	require.Equal(t, string(body), string(updated))
}

func TestApplyOpenAIFastPolicyToBody_GroupForceOnlyTargetsOpenAIAccounts(t *testing.T) {
	svc := newOpenAIGatewayServiceWithSettings(t, DefaultOpenAIFastPolicySettings())
	body := []byte(`{"model":"grok-4.1"}`)
	ctx := context.WithValue(context.Background(), ctxkey.Group, &Group{
		ID: 7, Platform: PlatformComposite, Status: StatusActive, Hydrated: true, ForceOpenAIFast: true,
	})

	updated, err := svc.applyOpenAIFastPolicyToBody(
		ctx,
		&Account{Platform: PlatformGrok, Type: AccountTypeOAuth},
		"grok-4.1",
		body,
	)
	require.NoError(t, err)
	require.Equal(t, string(body), string(updated))
}

func TestApplyOpenAIFastPolicyToBody_GroupForceRequiresSupportedGroupPlatform(t *testing.T) {
	svc := newOpenAIGatewayServiceWithSettings(t, DefaultOpenAIFastPolicySettings())
	body := []byte(`{"model":"gpt-5.6-sol"}`)
	ctx := context.WithValue(context.Background(), ctxkey.Group, &Group{
		ID: 7, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true, ForceOpenAIFast: true,
	})

	updated, err := svc.applyOpenAIFastPolicyToBody(
		ctx,
		&Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth},
		"gpt-5.6-sol",
		body,
	)
	require.NoError(t, err)
	require.Equal(t, string(body), string(updated))
}

// TestApplyOpenAIFastPolicyToBody_OfficialTiersBypassDefaultRule 验证默认配置
// 下客户端显式发送的 OpenAI 官方合法 tier 能透传到上游而不被静默剥离。
func TestApplyOpenAIFastPolicyToBody_OfficialTiersBypassDefaultRule(t *testing.T) {
	svc := newOpenAIGatewayServiceWithSettings(t, DefaultOpenAIFastPolicySettings())
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	for _, tier := range []string{"auto", "default", "scale"} {
		body := []byte(`{"model":"gpt-5.5","service_tier":"` + tier + `"}`)
		updated, err := svc.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-5.5", body)
		require.NoError(t, err, "tier %q should pass without error", tier)
		require.Contains(t, string(updated), `"service_tier":"`+tier+`"`,
			"tier %q should be preserved in body under default policy", tier)
	}

	// evaluate 层也应判定为 pass（默认配置没有内置规则）。
	for _, tier := range []string{"auto", "default", "scale"} {
		action, _ := svc.evaluateOpenAIFastPolicy(context.Background(), account, "gpt-5.5", tier)
		require.Equal(t, BetaPolicyActionPass, action, "tier %q should evaluate to pass", tier)
	}
}

// TestApplyOpenAIFastPolicyToBody_AllRuleStripsOfficialTiers 验证管理员显式配置
// ServiceTier=all + Action=filter 规则后，auto/default/scale 等官方 tier 也会
// 被剥离。这是符合预期的——首条匹配 short-circuit，"all" 覆盖任意已识别 tier。
func TestApplyOpenAIFastPolicyToBody_AllRuleStripsOfficialTiers(t *testing.T) {
	settings := &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier: OpenAIFastTierAny,
			Action:      BetaPolicyActionFilter,
			Scope:       BetaPolicyScopeAll,
		}},
	}
	svc := newOpenAIGatewayServiceWithSettings(t, settings)
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	for _, tier := range []string{"auto", "default", "scale", "priority", "flex"} {
		body := []byte(`{"model":"gpt-5.5","service_tier":"` + tier + `"}`)
		updated, err := svc.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-5.5", body)
		require.NoError(t, err)
		require.NotContains(t, string(updated), `"service_tier"`,
			"tier %q should be stripped under ServiceTier=all + filter rule", tier)
	}
}

// TestApplyOpenAIFastPolicyToBody_UnknownTierStripped 验证真未知 tier 仍被剥离
// （normalize 返回 nil → normalizeResponsesBodyServiceTier 删除字段；
// applyOpenAIFastPolicyToBody 在 normTier 为空时直接 no-op，因为字段已不可能存在
// 于经过前置归一化的请求里。这里直接调 apply 验证它对未识别值不会异常）。
func TestApplyOpenAIFastPolicyToBody_UnknownTierStripped(t *testing.T) {
	svc := newOpenAIGatewayServiceWithSettings(t, DefaultOpenAIFastPolicySettings())
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	// normalize 阶段会将未知值剥离
	require.Nil(t, normalizeOpenAIServiceTier("xxx"))

	// applyOpenAIFastPolicyToBody 收到未识别 tier 时不报错，body 透传不变
	// （不属于本函数职责——上层 normalizeResponsesBodyServiceTier 已剥离）
	body := []byte(`{"model":"gpt-5.5","service_tier":"xxx"}`)
	updated, err := svc.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-5.5", body)
	require.NoError(t, err)
	require.Equal(t, string(body), string(updated))
}

func TestApplyOpenAIFastPolicyToBody_BlockReturnsTypedError(t *testing.T) {
	settings := &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier:    OpenAIFastTierPriority,
			Action:         BetaPolicyActionBlock,
			Scope:          BetaPolicyScopeAll,
			ErrorMessage:   "fast mode is blocked for gpt-5.5",
			ModelWhitelist: []string{"gpt-5.5"},
			FallbackAction: BetaPolicyActionPass,
		}},
	}
	svc := newOpenAIGatewayServiceWithSettings(t, settings)
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	body := []byte(`{"model":"gpt-5.5","service_tier":"priority"}`)
	updated, err := svc.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-5.5", body)
	require.Error(t, err)
	var blocked *OpenAIFastBlockedError
	require.True(t, errors.As(err, &blocked))
	require.Contains(t, blocked.Message, "fast mode is blocked")
	require.Equal(t, string(body), string(updated)) // body not mutated on block
}

func TestApplyOpenAIFastPolicyToBody_NonOpenAIAccountBypassesPolicy(t *testing.T) {
	settings := &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{
			{
				ServiceTier:    OpenAIFastTierPriority,
				Action:         BetaPolicyActionBlock,
				Scope:          BetaPolicyScopeOAuth,
				ModelWhitelist: []string{"grok-4.6"},
				FallbackAction: BetaPolicyActionPass,
			},
			{
				ServiceTier: OpenAIFastTierAny,
				Action:      BetaPolicyActionFilter,
				Scope:       BetaPolicyScopeOAuth,
			},
		},
	}
	svc := newOpenAIGatewayServiceWithSettings(t, settings)
	account := &Account{Platform: PlatformGrok, Type: AccountTypeOAuth}
	body := []byte(`{"model":"grok-4.6","service_tier":"priority"}`)

	updated, err := svc.applyOpenAIFastPolicyToBody(context.Background(), account, "grok-4.6", body)
	require.NoError(t, err)
	require.Equal(t, string(body), string(updated))
}

func TestSetOpenAIFastPolicySettings_Validation(t *testing.T) {
	repo := &openAIFastPolicyRepoStub{values: map[string]string{}}
	svc := NewSettingService(repo, &config.Config{})

	// Invalid action rejected
	err := svc.SetOpenAIFastPolicySettings(context.Background(), &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier: OpenAIFastTierPriority,
			Action:      "bogus",
			Scope:       BetaPolicyScopeAll,
		}},
	})
	require.Error(t, err)

	// Invalid service_tier rejected
	err = svc.SetOpenAIFastPolicySettings(context.Background(), &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier: "turbo",
			Action:      BetaPolicyActionPass,
			Scope:       BetaPolicyScopeAll,
		}},
	})
	require.Error(t, err)

	// Non-positive and duplicate user IDs are rejected.
	err = svc.SetOpenAIFastPolicySettings(context.Background(), &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier: OpenAIFastTierPriority,
			Action:      BetaPolicyActionPass,
			Scope:       BetaPolicyScopeAll,
			UserIDs:     []int64{0},
		}},
	})
	require.Error(t, err)

	err = svc.SetOpenAIFastPolicySettings(context.Background(), &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier: OpenAIFastTierPriority,
			Action:      BetaPolicyActionPass,
			Scope:       BetaPolicyScopeAll,
			UserIDs:     []int64{42, 42},
		}},
	})
	require.Error(t, err)

	// Missing-tier injection is only valid for all + force_priority.
	err = svc.SetOpenAIFastPolicySettings(context.Background(), &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier:             OpenAIFastTierPriority,
			Action:                  OpenAIFastPolicyActionForcePriority,
			Scope:                   BetaPolicyScopeAll,
			InjectPriorityIfMissing: true,
		}},
	})
	require.Error(t, err)

	err = svc.SetOpenAIFastPolicySettings(context.Background(), &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier:             OpenAIFastTierAny,
			Action:                  BetaPolicyActionPass,
			Scope:                   BetaPolicyScopeAll,
			InjectPriorityIfMissing: true,
		}},
	})
	require.Error(t, err)

	// Valid settings persisted
	err = svc.SetOpenAIFastPolicySettings(context.Background(), &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier:             OpenAIFastTierAny,
			Action:                  OpenAIFastPolicyActionForcePriority,
			Scope:                   BetaPolicyScopeAll,
			InjectPriorityIfMissing: true,
			UserIDs:                 []int64{42, 43},
		}},
	})
	require.NoError(t, err)

	got, err := svc.GetOpenAIFastPolicySettings(context.Background())
	require.NoError(t, err)
	require.Len(t, got.Rules, 1)
	require.Equal(t, OpenAIFastTierAny, got.Rules[0].ServiceTier)
	require.Equal(t, OpenAIFastPolicyActionForcePriority, got.Rules[0].Action)
	require.True(t, got.Rules[0].InjectPriorityIfMissing)
	require.Equal(t, []int64{42, 43}, got.Rules[0].UserIDs)
}

func TestGetOpenAIFastPolicySettings_CachesReadsAndSetRefreshesCache(t *testing.T) {
	repo := &openAIFastPolicyRepoStub{values: map[string]string{}}
	initial := &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier: OpenAIFastTierPriority,
			Action:      BetaPolicyActionPass,
			Scope:       BetaPolicyScopeAll,
		}},
	}
	raw, err := json.Marshal(initial)
	require.NoError(t, err)
	repo.values[SettingKeyOpenAIFastPolicySettings] = string(raw)
	svc := NewSettingService(repo, &config.Config{})

	first, err := svc.GetOpenAIFastPolicySettings(context.Background())
	require.NoError(t, err)
	second, err := svc.GetOpenAIFastPolicySettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, 1, repo.getValueCalls)

	updated := &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier:             OpenAIFastTierAny,
			Action:                  OpenAIFastPolicyActionForcePriority,
			Scope:                   BetaPolicyScopeAll,
			InjectPriorityIfMissing: true,
		}},
	}
	require.NoError(t, svc.SetOpenAIFastPolicySettings(context.Background(), updated))

	got, err := svc.GetOpenAIFastPolicySettings(context.Background())
	require.NoError(t, err)
	require.Len(t, got.Rules, 1)
	require.True(t, got.Rules[0].InjectPriorityIfMissing)
	require.Equal(t, 1, repo.getValueCalls, "save should update the local cache without another DB read")

	got.Rules[0].Action = BetaPolicyActionBlock
	got.Rules[0].ModelWhitelist = append(got.Rules[0].ModelWhitelist, "mutated")
	again, err := svc.GetOpenAIFastPolicySettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, OpenAIFastPolicyActionForcePriority, again.Rules[0].Action)
	require.Empty(t, again.Rules[0].ModelWhitelist, "callers must not mutate the cached settings")

	updated.Rules[0].Action = BetaPolicyActionPass
	afterCallerMutation, err := svc.GetOpenAIFastPolicySettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, OpenAIFastPolicyActionForcePriority, afterCallerMutation.Rules[0].Action,
		"mutating the value passed to Set must not mutate the cache")
}

func TestGetOpenAIFastPolicySettings_ConcurrentSetWinsOverInflightRead(t *testing.T) {
	initial := &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier: OpenAIFastTierPriority,
			Action:      BetaPolicyActionPass,
			Scope:       BetaPolicyScopeAll,
		}},
	}
	initialJSON, err := json.Marshal(initial)
	require.NoError(t, err)
	repo := &blockingOpenAIFastPolicyRepo{
		openAIFastPolicyRepoStub: &openAIFastPolicyRepoStub{},
		value:                    string(initialJSON),
		readStarted:              make(chan struct{}),
		releaseRead:              make(chan struct{}),
	}
	svc := NewSettingService(repo, &config.Config{})

	type readResult struct {
		settings *OpenAIFastPolicySettings
		err      error
	}
	resultCh := make(chan readResult, 1)
	go func() {
		settings, readErr := svc.GetOpenAIFastPolicySettings(context.Background())
		resultCh <- readResult{settings: settings, err: readErr}
	}()
	<-repo.readStarted

	updated := &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier:             OpenAIFastTierAny,
			Action:                  OpenAIFastPolicyActionForcePriority,
			Scope:                   BetaPolicyScopeAll,
			InjectPriorityIfMissing: true,
		}},
	}
	require.NoError(t, svc.SetOpenAIFastPolicySettings(context.Background(), updated))
	close(repo.releaseRead)

	result := <-resultCh
	require.NoError(t, result.err)
	require.Len(t, result.settings.Rules, 1)
	require.Equal(t, OpenAIFastPolicyActionForcePriority, result.settings.Rules[0].Action)
	require.True(t, result.settings.Rules[0].InjectPriorityIfMissing)

	cached, err := svc.GetOpenAIFastPolicySettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, OpenAIFastPolicyActionForcePriority, cached.Rules[0].Action)
}

func TestGetOpenAIFastPolicySettings_ColdReadHonorsCancellation(t *testing.T) {
	repo := &blockingOpenAIFastPolicyRepo{
		openAIFastPolicyRepoStub: &openAIFastPolicyRepoStub{},
		readStarted:              make(chan struct{}),
		releaseRead:              make(chan struct{}),
	}
	svc := NewSettingService(repo, &config.Config{})
	ctx, cancel := context.WithCancel(context.Background())

	resultCh := make(chan error, 1)
	go func() {
		_, err := svc.GetOpenAIFastPolicySettings(ctx)
		resultCh <- err
	}()
	<-repo.readStarted
	cancel()

	require.ErrorIs(t, <-resultCh, context.Canceled)
	close(repo.releaseRead)
}

func TestGetOpenAIFastPolicySettings_LeaderCancellationDoesNotPoisonWaiters(t *testing.T) {
	settings := &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier: OpenAIFastTierPriority,
			Action:      BetaPolicyActionPass,
			Scope:       BetaPolicyScopeAll,
		}},
	}
	settingsJSON, err := json.Marshal(settings)
	require.NoError(t, err)
	repo := &blockingOpenAIFastPolicyRepo{
		openAIFastPolicyRepoStub: &openAIFastPolicyRepoStub{},
		value:                    string(settingsJSON),
		readStarted:              make(chan struct{}),
		releaseRead:              make(chan struct{}),
	}
	svc := NewSettingService(repo, &config.Config{})
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	waiterCtx := &observedDoneContext{
		Context:      context.Background(),
		doneObserved: make(chan struct{}),
	}

	leaderResult := make(chan error, 1)
	go func() {
		_, readErr := svc.GetOpenAIFastPolicySettings(leaderCtx)
		leaderResult <- readErr
	}()
	<-repo.readStarted

	waiterResult := make(chan struct {
		settings *OpenAIFastPolicySettings
		err      error
	}, 1)
	go func() {
		got, readErr := svc.GetOpenAIFastPolicySettings(waiterCtx)
		waiterResult <- struct {
			settings *OpenAIFastPolicySettings
			err      error
		}{settings: got, err: readErr}
	}()
	<-waiterCtx.doneObserved
	cancelLeader()
	require.ErrorIs(t, <-leaderResult, context.Canceled)
	close(repo.releaseRead)

	waiter := <-waiterResult
	require.NoError(t, waiter.err)
	require.Equal(t, settings, waiter.settings)
}
