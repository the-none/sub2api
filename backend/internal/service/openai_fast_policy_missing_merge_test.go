package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIFastPolicy_MissingMatcherPreservesInjectionBoundaries(t *testing.T) {
	svc := newOpenAIGatewayServiceWithSettings(t, &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{
			{ServiceTier: OpenAIFastTierMissing, Action: OpenAIFastPolicyActionForcePriority, Scope: BetaPolicyScopeAll},
			{ServiceTier: OpenAIFastTierPriority, Action: BetaPolicyActionBlock, Scope: BetaPolicyScopeAll},
		},
	})
	for _, tc := range []struct {
		name    string
		baseURL string
		tier    string
		inject  bool
	}{
		{name: "missing is not reevaluated as explicit priority", inject: true},
		{name: "null remains explicit", tier: `,"service_tier":null`},
		{name: "empty remains explicit", tier: `,"service_tier":""`},
		{name: "third party remains unchanged", baseURL: "https://api.deepseek.com/v1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
			if tc.baseURL != "" {
				account.Credentials = map[string]any{"base_url": tc.baseURL}
			}
			body := []byte(`{"type":"response.create","model":"gpt-5.5"` + tc.tier + `}`)
			httpBody, err := svc.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-5.5", body)
			require.NoError(t, err)
			wsBody, blocked, err := svc.applyOpenAIFastPolicyToWSResponseCreate(context.Background(), account, "gpt-5.5", body)
			require.NoError(t, err)
			require.Nil(t, blocked)
			for _, got := range [][]byte{httpBody, wsBody} {
				if tc.inject {
					require.Equal(t, OpenAIFastTierPriority, gjson.GetBytes(got, "service_tier").String())
				} else {
					require.JSONEq(t, string(body), string(got))
				}
			}
		})
	}
	// Protocol conversion may strip an explicit null. It must not turn into
	// an opt-in missing-tier request merely because the normalized body is empty.
	body := []byte(`{"model":"gpt-5.5"}`)
	got, err := svc.applyOpenAIFastPolicyToNormalizedBody(context.Background(), &Account{
		Platform: PlatformOpenAI, Type: AccountTypeOAuth,
	}, "gpt-5.5", body, true)
	require.NoError(t, err)
	require.JSONEq(t, string(body), string(got))
}
