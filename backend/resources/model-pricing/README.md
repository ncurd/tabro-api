# Model Pricing Data

This directory contains a local copy of the mirrored model pricing data as a fallback mechanism.

## Source
The original file is maintained by the LiteLLM project and mirrored into the `price-mirror` branch of this repository via GitHub Actions:
- Mirror branch (configurable via `PRICE_MIRROR_REPO`): https://raw.githubusercontent.com/<your-repo>/price-mirror/model_prices_and_context_window.json
- Upstream source: https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json

## Purpose
This local copy serves as a fallback when the remote file cannot be downloaded due to:
- Network restrictions
- Firewall rules
- DNS resolution issues
- GitHub being blocked in certain regions
- Docker container network limitations

## Update Process
The pricingService will:
1. First attempt to download the latest version from GitHub
2. If download fails, use this local copy as fallback
3. Log a warning when using the fallback file

## Manual Update
To manually update this file with the latest pricing data (if automation is unavailable):
```bash
curl -s https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json -o model_prices_and_context_window.json
```

## File Format
The file contains JSON data with model pricing information including:
- Model names and identifiers
- Input/output token costs
- Context window sizes
- Model capabilities

GPT-6.1 Sol, GPT-6 Sol, GPT-6 Luna, and Claude Opus 5.5 entries use official provider pricing:
- OpenAI: https://developers.openai.com/api/docs/pricing
- Anthropic: https://platform.claude.com/docs/en/about-claude/pricing

GPT-6.1 Sol and GPT-6 Sol/Luna include standard, priority, flex, batch, and over-272K context rates. GPT-6.1 Sol cache reads cost $0.10 per million tokens at standard rates; GPT-6 Sol cache reads cost $0.20.
Opus 5.5 includes 5-minute/1-hour cache writes, cache reads at 5% of input, and the 2x fast multiplier. It has no long-context surcharge.

The same catalog retains explicit `max_input_tokens` / `max_output_tokens` for central billing admission. GPT-6.1 Sol, GPT-6 Sol, and GPT-6 Luna allow 922,000 input and 128,000 output tokens; Opus 5.5 allows 1,000,000 context and 128,000 synchronous output tokens. Sources:
- https://developers.openai.com/api/docs/models/gpt-6.1-sol
- https://developers.openai.com/api/docs/models/gpt-6-sol
- https://developers.openai.com/api/docs/models/gpt-6-luna
- https://platform.claude.com/docs/en/models/opus-5-5/overview

Admission requires an exact capability entry. Price-family fuzzy matching is not a safe bound for unknown models. Codex OAuth strips client output limits, so a request that may use OAuth reserves the full catalogued output cap. API-key Responses retains its executable limit. No-max requests receive the model cap; all text requests reserve the full input cap to cover transformed instructions, vision and WebSocket continuations. Settlement still charges actual usage and releases the difference.

Last updated: 2026-09-30
