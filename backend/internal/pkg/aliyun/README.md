# Aliyun text model catalogue

Verified against Alibaba Cloud Model Studio documentation on 2026-11-18. Each
record in `text_models.json` includes its official source and any limitations.

The catalogue stores input limits, output limits and context windows separately.
They are independent bounds: input and output maxima must not be added to infer
context capacity. Thinking and non-thinking limits remain separate where the
provider documents a distinction. An absent limit remains unknown.

Bare Model Studio IDs and supplier-qualified IDs are distinct records. For
example, `glm-5` and `ZHIPU/GLM-5` have different hosted limits. Only the explicit
`aliyun/` and `dashscope/` namespaces resolve to the same Model Studio record;
arbitrary aliases, unlisted snapshots and guessed suffixes do not inherit limits.

`default_suggestion` controls the recommended model list. Historical model IDs
can retain verified limits for existing account mappings without being suggested
for new configurations. Availability can also depend on region and subscription.

This file contains capabilities only. It never installs a zero price or changes
pricing. Capability discovery and credit reservation can therefore work even
when a remote pricing mirror has not added the model yet; calls still need a
configured or otherwise available price.

The frontend suggestion list is derived from the same records and checked by
`TestFrontendAndBackendAliyunSuggestionsStayInSync`. After updating the catalogue,
regenerate `frontend/src/data/aliyunTextModels.json` from the IDs whose
`default_suggestion` is true, preserving catalogue order.
