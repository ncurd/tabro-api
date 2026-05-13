# User Model Pricing Credits Display Design

## Context

Users need a model pricing page that shows prices for models available under the groups they can use. The pricing source is the existing billing model price multiplied by the effective group rate. The platform currently displays many internal billing values with a dollar sign, but the product should present these internal billing values as credits. One credit equals the current internal dollar-denominated value at a 1:1 ratio.

## Scope

Build a logged-in model pricing page backed by current user availability. Change frontend display of internal platform billing values from `$` to credits. Keep backend field names, persisted values, API units, and existing compatibility responses unchanged. Any new pricing endpoint returns existing numeric billing values only; the credit unit is applied in the frontend presentation layer.

This scope does not change payment currency. Real payment amounts that use `¥` remain currency amounts. Backend JSON fields such as `actual_cost`, `balance`, `daily_limit_usd`, and compatibility endpoint `unit` values stay as they are for compatibility.

## Data Source

The page uses current-user group availability, matching the existing user-visible group rules:

- Standard non-exclusive groups available to the user.
- Exclusive groups explicitly allowed for the user.
- Subscription groups available through the user's active subscription.

For each available group, the available model list comes from actual schedulable accounts in that group:

- Aggregate `model_mapping` keys from schedulable accounts.
- If no account has a model mapping, fall back to the existing platform default model list.
- Sort models by ID for deterministic display.

For each model, resolve the base model price through the existing billing pricing path:

- Prefer dynamic LiteLLM pricing loaded by `PricingService`.
- Use existing billing fallback prices when dynamic data is missing.
- Preserve the existing channel pricing behavior where the billing resolver already applies it.

The display price is:

`resolved base price * effective group rate`

The effective group rate is the current user's custom group rate when present, otherwise the group's `rate_multiplier`.

## API Design

Add an authenticated endpoint such as:

`GET /api/v1/model-pricing/available`

Response shape:

```json
{
  "groups": [
    {
      "id": 1,
      "name": "Default",
      "platform": "openai",
      "rate_multiplier": 1,
      "effective_rate_multiplier": 1,
      "models": [
        {
          "id": "gpt-5.4",
          "pricing_available": true,
          "input_price_per_million": 2.5,
          "output_price_per_million": 15,
          "cache_write_price_per_million": 2.5,
          "cache_read_price_per_million": 0.25,
          "priority_input_price_per_million": 5,
          "priority_output_price_per_million": 30,
          "source": "litellm"
        }
      ]
    }
  ]
}
```

The endpoint returns numeric values only. It does not label them as USD or credits. Frontend presentation converts those numbers to credits.

Unknown-price models stay in the list with `pricing_available: false`; the page displays a missing-price state for that row instead of failing the whole request.

## Frontend Design

Add a user route such as `/model-pricing` or `/pricing`. The page is a logged-in utility screen, not a landing page.

Primary UI:

- Group filter tabs or a compact select for available groups.
- Search input for model IDs.
- Table with model ID, input, output, cache write, cache read, priority input, and priority output.
- Empty states for no available groups, no available models, and no matching search results.
- Missing-price rows show `暂无价格`.

Pricing display:

- Show prices as `积分 / 1M tokens`.
- Use a small credit icon in visible UI where practical.
- Use plain `积分` text in tables, tooltips, charts, CSV, and Excel exports where icons are not appropriate.

## Frontend-Wide Credits Display

Introduce a shared formatter such as `formatCredits(value, options)` and a small `CreditAmount` component for UI surfaces. Replace user-facing `$` usage for internal platform billing values with credits:

- User dashboard balance and usage costs.
- User usage table and cost tooltips.
- Admin dashboard cost summaries and charts.
- Group/model/endpoint distribution charts.
- API key quota and rate limit displays.
- Balance notification threshold display.
- Payment order credited balance amount.
- Recharge balance amount input, because that amount buys platform credits.

Do not replace:

- Shell prompt examples that intentionally show `$`.
- JavaScript template syntax and regex replacement strings.
- Real payment currency `¥`.
- Backend comments, tests, JSON field names, or API compatibility unit values.

## Error Handling

If the pricing endpoint fails, the page shows a retryable error state. If one model's price is unavailable, only that row is marked unavailable. If group or model availability cannot be loaded for a group, the endpoint should skip only the failing group when possible and include no partial backend error details in the user-visible response.

## Testing

Backend tests:

- Current-user available groups determine returned groups.
- Schedulable account `model_mapping` keys determine returned models.
- No mapping falls back to platform defaults.
- Effective group rate uses user custom rate when present.
- Per-million prices are multiplied by the effective rate.
- Unknown model returns `pricing_available: false`.

Frontend tests:

- Credits formatter formats fixed and small values correctly.
- Pricing page renders group filters, model rows, empty states, and missing-price rows.
- Existing user dashboard and usage views no longer render `$` for internal billing values.
- Payment `¥` displays are unchanged.

## Route Decision

The preferred route label is `模型价格`. The exact path can be `/model-pricing` to avoid ambiguity with payment/recharge pricing.
