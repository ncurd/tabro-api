# LLM 网关 OIDC Resource Server 配置与迁移指南

本文说明如何让 Tabro 的 LLM 网关以 OAuth 2.0 Resource Server 方式接收访问令牌。`gateway.resource_server.auto_provision` 开启后，网关首次收到有效的外部 Bearer Token 时自动建立内部路由与用量身份，客户扣费由 Auth 统一计费中心负责。旧有手动绑定路径继续保留，资金权威按模式隔离。中心计费部署和迁移要求见 [网关中心计费实施说明](BILLING_CENTER_GATEWAY_CN.md)。

默认全平台分组、Key 的全部公开/多分组范围与升级兼容行为见 [分组路由说明](GROUP_ROUTING_CN.md)。

## 先区分两种 OIDC 用途

`oidc_connect` 和 `gateway.resource_server` 是两套独立的安全边界。它们可以使用同一个身份提供方（IdP），但 `gateway.resource_server` 本身只是令牌验证方，不需要 Client registration 或 Client Secret。`oidc_connect` 应保留已有的后台登录 Client；Agent 的授权码、刷新和 Token Exchange 则复用已有的 confidential `tabro-agent` Client。不要为了网关验证或 Token Exchange 再创建重复 Client，也不能混用两条链路的登录令牌、网关令牌或权限。Client Secret 始终只能由受信任后端持有。

| 用途 | 配置位置 | 令牌给谁使用 | 主要作用 |
|---|---|---|---|
| Tabro 网页登录 | `oidc_connect` | Tabro UI 和 `/api/v1/...` 管理 API | 完成浏览器登录；回调成功后由 Tabro 签发本地登录令牌 |
| LLM 网关调用 | `gateway.resource_server` | `/v1/...`、`/v1beta/...` 等模型网关端点 | 验证 IdP 签发、且唯一 audience 为 LLM 网关的 access token |

重要约束：

- `oidc_connect` 登录后得到的 Tabro 本地 `access_token` 不是 LLM 网关令牌，不能用来调用模型端点。
- 网关 OAuth access token 必须由受信任 IdP 签发，且 `aud` 只能是网关自己的 audience；Tabro 的统一约定为 `tabro-llm`。
- 普通 Tabro API Key 仍可按原方式调用网关；外部 OAuth JWT 只能放在 `Authorization: Bearer` 中，不能放在 `x-api-key`、`x-goog-api-key` 或查询参数中。
- 为防止外部 JWT 或 Tabro 本地 JWT 退回 API Key 路径，任何恰好包含两个 `.`、外形类似 JWT 的旧 API Key 都会被拒绝；这类导入 Key 必须在升级前轮换为非 JWT 外形的新 Key。
- 同一个请求不要同时携带 OAuth Bearer Token 和其他 API Key，否则会被当作冲突凭证拒绝。

## 仅 OIDC 网页登录模式

后台「系统设置 → OIDC 登录」提供 `oidc_only_enabled`。先保存并启用 `oidc_connect`，用 OIDC 重新登录**管理员**账号，确认可以进入后台，再开启此模式。开启请求必须来自这次 OIDC 管理员会话；同一次保存不能修改 OIDC 的关键配置。模式开启后若要更换身份提供方或回调配置，先关闭模式，再修改并验证配置。

开启后，Tabro 拒绝密码登录、注册、邮箱验证码、双因素补登、密码找回和 LinuxDo 登录；先前签发的非 OIDC 网页访问令牌和刷新令牌也会被拒绝。OIDC 登录、按现有注册策略进行的 OIDC 首次建户、登出，以及用于模型网关调用的 API Key 仍按各自规则工作。`registration_enabled` 同时控制 OIDC 首次建户：希望新 OIDC 用户自动创建时，应保持它开启，不能用它代替仅 OIDC 开关。

管理员 API Key 可用于在 IdP 故障时访问设置并关闭此模式；它不会自动生成。如需要这条恢复路径，应在切换前生成并妥善保存。

仅 OIDC 模式保存后，同一设置页可开启「OIDC 计费」并设置计费倍数、每日结算时间和时区。此功能还要求有效的 Auth 计费连接器、已启用网关 Resource Server，且两套 OIDC 配置的 issuer 相同。默认按北京时间 00:00 结算，每次调用先锁定 Auth 积分，网关记录实际费用，到点逐笔结算；开启后停用本地余额、套餐与兑换券。具体公式、预扣占用、失败处理和迁移要求见 [每日计费说明](BILLING_CENTER_GATEWAY_CN.md#仅-oidc-模式下的每日计费)。

## 调用链路

```text
Tabro Client ──向 IdP 申请 access token──> IdP
      │
      │ Authorization: Bearer <aud=tabro-llm>
      ▼
LLM 网关
  1. 验证签名、iss、唯一 aud、exp/nbf、scope、Client 和 act
  2. 按已验证的 (iss, sub) 查找或自动建立内部路由 Key
  3. 自动模式将原始 Token 作为短暂的 subject proof 提交给 Auth
  4. Auth 核验身份、团队资格与余额/预算，Quote 选付款账户，Reserve 预占资金
  5. 网关取得执行许可，选择配置的上游账号
      │
      │ 使用网关自己保管的 OpenAI/Anthropic 等上游凭证
      ▼
模型供应商 ──返回实际 usage──> 网关记用量、Auth 结算并返回统一 usage
```

自动模式下，本地 Key 只承担分组路由、访问限制与用量外键，不是客户余额账户。入口 Bearer Token 不会转发给 OpenAI、Anthropic 或其他模型供应商，也不会写入网关账本或日志。

## 网关验证的 Token 条件

网关采用 fail-closed 策略。一个 Token 必须同时满足以下条件：

- JWT 签名可由配置的 JWKS 验证，且算法属于 `RS256`、`ES256`、`PS256` 中显式允许的算法。
- Discovery/JWKS 地址由固定配置或可信 Discovery 决定；HTTPS issuer 的每一跳重定向都禁止降级到 HTTP。
- `iss` 与 `gateway.resource_server.issuer_url` 完全一致。
- `aud` 只有一个值，且该值与 `gateway.resource_server.audience` 完全一致。
- 必须存在有效的 `exp`；如果存在 `nbf`，也必须已经生效。仅允许配置范围内的时钟偏差。
- `scope` 或 `scp` 包含所有 `required_scopes`，默认要求 `llm.invoke`。Scope 按完整词匹配，不做子串匹配。
- 顶层 `azp` 必须是非空字符串且位于 `allowed_client_ids` 中；`client_id` 不能替代它，若存在则必须与 `azp` 相同。
- `sub` 必须存在且非空。关闭自动模式时，`(iss, sub)` 必须已经绑定到有效的内部 API Key；开启时，首次有效调用会自动建立绑定。
- 如果启用了租户强制校验，配置的 tenant claim 必须存在。租户值只能来自已经验签的 Token。
- Token Exchange Token 必须携带有效的 `act` 委托信息；immediate `act.sub` 必须与顶层 `azp` 相同，每层 `act.sub` 都必须位于 actor allowlist 中，嵌套深度不能超过配置值。

不要采用以下“兼容”方式：

- 当前统一使用 `tabro-llm` 作为 Agent 登录 subject token 与模型交换 token 的 audience；两者依靠 `tabro.run` 与 `llm.invoke`/`act` 分层，不能只看 audience 放行。
- 不要签发同时包含 `tabro-llm` 和 `tabro-api` 的多 audience Token；即使其中包含网关 audience，也会被拒绝。
- 不要关闭 audience 校验来解决两个 Client ID 不同的问题。`aud` 表示资源，必需的顶层 `azp` 表示获准调用该资源的 Client，二者必须分别校验；`client_id` 不能替代 `azp`。
- 不要使用邮箱、用户名、`X-Tabro-*` Header 或 Token 中携带的本地 Key ID 推断计费身份。

## 在 IdP 中配置资源和 Client

不同 IdP 的界面名称可能是 API、Resource Server、Authorization Server、Audience、API Identifier 或 Resource Indicator，但应实现相同结果。

1. 创建一个专属于 LLM 网关的资源：

   - 统一 audience：`tabro-llm`
   - Auth、Agent Framework 和网关必须使用完全相同的 `tabro-llm`，不要改成 API URL 或 Client ID
   - 当前允许登录 subject token 与模型交换 token 复用该 audience；网关仍必须要求模型 Scope，并校验 `act`

2. 为该资源创建 Scope：

   - 必需：`llm.invoke`
   - 如果配置了多个 `required_scopes`，Token 必须包含全部 Scope

3. 更新或选择 Tabro 调用端 Client（当前直接使用已有的 `tabro-agent`，不要再创建一个仅用于 Token Exchange 的重复 Client）：

   - 当前调用应用 Client ID 为 `tabro-agent`
   - 将来增加 `tabro-drama` 时，把它作为独立 Client，并在 allowlist 中用逗号追加
   - 把它授权给 LLM 网关资源及 `llm.invoke` Scope
   - 确保 access token 中有稳定、可信的 `sub`
   - 确保 access token 中必需的顶层 `azp` 是该 Client ID；可选 `client_id` 若存在也必须相同
   - 把 Client ID 加入网关的 `allowed_client_ids`

4. 配置 access token：

   - 使用短生命周期，例如 5～15 分钟
   - 使用带 `kid` 的非对称签名 JWT
   - 发布标准 OIDC Discovery 和 JWKS，或向网关提供固定的 JWKS URL
   - 多租户计费时，加入稳定的签名 tenant claim，例如 `tenant_id`

`oidc_connect` 只负责 tabro-api 自身管理界面的单点登录，与 Agent 调用模型的资源服务器链路无关。若已经启用，请保持现有后台登录 Client；不要为了 Token Exchange 再创建一个 Client。配置示意：

```yaml
oidc_connect:
  enabled: true
  provider_name: "Company SSO"
  # 使用现有的后台登录 Client；这不是模型 Token Exchange Client
  client_id: "<existing-admin-login-client-id>"
  client_secret: "<existing-admin-login-client-secret>"
  issuer_url: "https://idp.example.com/realms/production"
  scopes: "openid email profile"
  redirect_url: "https://llm.example.com/api/v1/auth/oauth/oidc/callback"
  frontend_redirect_url: "/auth/oidc/callback"
  token_auth_method: "client_secret_post"
  validate_id_token: true
  allowed_signing_algs: "RS256,ES256,PS256"
  require_email_verified: true
```

Client Secret 只能由 Auth、Token Broker、Agent Framework 服务端等受信任后端持有，绝不能下发到浏览器或不受信任的前端。如果 Auth 按目标架构复用同一个 confidential `tabro-agent` registration，后端可以使用同一组客户端凭据完成授权码交换和 Token Exchange；但不要把 Secret 放进 `gateway.resource_server`，也不要把网页登录得到的 ID token 或 Tabro 本地登录令牌发给 LLM 网关。

### Token Exchange / 委托调用

如果 Tabro 通过 OAuth 2.0 Token Exchange 代表另一个 Client 或服务调用网关：

- 当前 Tabro Auth 约定 subject token 和 exchanged token 的 `aud` 都是 `tabro-llm`。交互登录流程不能直接取得 `llm.invoke`；只有 exchange 后的 Token 才携带该 Scope 和 `act`。
- Auth 在 exchanged token 中用 `azp` 标识调用应用，并按三仓约定生成 `act: {"sub":"<调用应用 Client ID>"}`，例如 `azp: "tabro-agent"` 和 `act: {"sub":"tabro-agent"}`。未来 `tabro-drama` 调用时，两处值都应为 `tabro-drama`。
- IdP 应在 `gty` 或 `grant_type` 中标记 Token Exchange，并生成 RFC 8693 风格的 `act` 对象。
- 每一层 `act` 都必须用非空字符串 `sub` 标识 actor；`client_id` 或 `azp` 不能替代 `sub`。旧显式绑定路径可按配置校验嵌套委托；自动中心计费与当前 Auth subject proof 契约只接受单层。
- 同一层 `act` 若额外携带 `client_id` 或 `azp`，它们必须是非空字符串并与该层 `sub` 完全一致；任一字段类型错误、为空或值冲突都会使整个 Token 被拒绝。
- 调用应用必须同时加入 `allowed_client_ids`；Token Exchange 中的每一层 actor ID 还必须加入 `allowed_actor_client_ids`。
- 只要 Token 携带 `act`，actor allowlist 就不能留空；留空会拒绝该 Token。
- 自动中心计费必须设置 `require_actor: true`，强制所有网关 Token 都携带 `act`。如果还要接受没有 `act` 的 `client_credentials` 服务 Token，应关闭 `auto_provision` 并按旧显式绑定路径单独部署；只要 Token 携带 `act`，actor 仍会严格校验且空 allowlist 会拒绝。
- 将 `max_delegation_depth` 保持在业务实际需要的最小值。

## 网关配置

### YAML 配置

Resource Server 的 issuer、audience、Scope 和身份 allowlist 是一组需要整体审阅的信任策略，**只允许在 `config.yaml` 中配置**。进程会拒绝非空的 `GATEWAY_RESOURCE_SERVER_*` 环境变量，避免容器环境悄悄覆盖 YAML；其他通用环境变量的加载方式不变。

在 `config.yaml` 中加入：

```yaml
gateway:
  resource_server:
    enabled: true
    # 必须与 Token 的 iss 完全一致，包括路径和末尾斜杠
    issuer_url: "https://idp.example.com/realms/production"

    # 两项均为空时，默认读取：
    # <issuer_url>/.well-known/openid-configuration
    discovery_url: ""
    jwks_url: ""

    # 必须是网关专属、唯一的 audience
    audience: "tabro-llm"
    required_scopes: "llm.invoke"

    # 逗号、分号或空格分隔；新增应用时可改为 "tabro-agent,tabro-drama"
    allowed_client_ids: "tabro-agent"
    allowed_signing_algs: "RS256,ES256,PS256"
    clock_skew_seconds: 120
    jwks_cache_ttl_seconds: 300

    # Auth 中心计费自动绑定：需要同时启用 billing_center，所有有效的外部
    # OIDC Bearer 都走中心计费；旧的非中心显式 route 会拒绝而非本地扣费
    auto_provision: true

    # Auth 中心计费要求稳定、已签名的租户 claim
    tenant_claim: "tenant_id"
    require_tenant: true

    token_exchange:
      # Agent/Drama 的 Token Exchange 部署必须为 true；仅在明确还要接受
      # client_credentials Token 时才可按独立风险评估改为 false
      require_actor: true
      actor_claim: "act"
      # 显式 allowlist；留空会拒绝所有携带 act 的 Token
      allowed_actor_client_ids: "tabro-agent"
      # Auth 的 subject proof 只接受单层 Token Exchange 委托
      max_delegation_depth: 1
```

自动中心计费要求 `require_tenant: true`，并把 `tenant_claim` 设置成 Auth 实际签发且始终非空的 claim 名；`token_exchange.require_actor` 必须为 `true`，`max_delegation_depth` 必须为 `1`，与 Auth 对 subject proof 的校验一致。旧的显式绑定路径可以继续按原策略运行，但不要把无租户的 Token 用于自动中心扣费。

生产环境应使用 HTTPS issuer、Discovery 和 JWKS。`issuer_url` 不能从请求 Token 动态决定；必须由运维配置固定。

### Docker Compose

不要把这组配置写入 `.env`。复制完整 YAML 示例，修改其中的 `gateway.resource_server`，并在所用 Compose 文件中启用已经预留的单文件挂载：

```bash
cd deploy
cp config.example.yaml config.yaml
# 编辑 config.yaml，并取消 Compose 中 ./config.yaml:/app/data/config.yaml 的注释
```

已有安装也可以直接更新持久化数据卷里的 `/app/data/config.yaml`。完成后重建或重启服务：

```bash
docker compose up -d --force-recreate
```

## Tabro Agent 侧怎么配置

不要通过 Compose 环境变量配置这条认证链。登录 OIDC 与模型 Provider 都应在 Tabro Agent 的安装/设置页面中完成；页面提交的 confidential `tabro-agent` Client Secret 由 Control Plane 写入受限的安装密钥文件，不会进入浏览器持久化、Runtime Worker 或本 Resource Server 配置。subject token 和 exchanged token 的 audience 都统一为 `tabro-llm`，但只有 exchanged token 才有 `llm.invoke` 和 `act`。

在 Agent 的安装页面选择 OIDC 登录并填写已有的 `tabro-agent` Client ID、Client Secret、Authority、回调地址和登录 Scope；模型 Provider 选择 `oauth-token-exchange`。对应的非秘密模型参数如下：

```json
{
  "name": "LLM Gateway OpenAI",
  "slug": "llm-gateway-openai",
  "protocol": "openai-compatible",
  "baseUrl": "https://llm.example.com/v1",
  "authMode": "oauth-token-exchange",
  "issuer": "https://idp.example.com/realms/production",
  "clientId": "tabro-agent",
  "audience": "tabro-llm",
  "scopes": "llm.invoke",
  "clientAuthMethod": "client_secret_basic",
  "requiresApiKey": false,
  "enabled": true
}
```

Token Exchange 不配置第二份模型 Client Secret；它复用安装页面中已有的 OIDC Secret。Control Plane 执行交换，Runtime Worker 只拿安装程序自动生成的 broker key。Anthropic Messages 应另建一个 `protocol: "anthropic-messages"` 的 Provider，OAuth 参数可以相同。不要把短期 access token 当作模型 API Key 保存，也不要保留会让模型目录缺失时意外直连供应商的 OpenAI/Anthropic Key fallback。

如果确实需要独立的 `client_credentials` 服务身份模式，应另用 `tabro-model-service`，在 Agent 设置页面填写并由服务端加密保存它自己的 Client Secret；按旧显式绑定路径部署，关闭 `auto_provision` 并设置 `require_actor: false` 以接受不带 `act` 的服务 Token。它不是当前 `tabro-agent` 的用户委托模式，也不要因为它没有 `act` 就把它加入 actor allowlist。

认证模式的选择会直接影响计费身份：

- `oauth-client-credentials` 的 `sub` 是服务身份，只适合把费用统一记到 Tabro 服务账号。
- 要按最终用户计费，应使用 `oauth-token-exchange`：交换后的 Token 保留可信用户 `sub`，`aud` 为 `tabro-llm`，并以 `azp` 和可验证的 `act` 委托链标识调用应用。用户登录 Token 只交给受信任的 Token Broker，不能直接发送给 LLM 网关或模型供应商。

已有安装必须通过 Agent 的 Model Provider 设置或随版本提供的离线安装配置迁移更新，不能只改 Compose。切流前应检查安装文件、页面显示的非秘密配置和最终签发 Token 是否一致。

## 身份自动绑定与计费

设置 `gateway.resource_server.auto_provision: true` 后，所有通过 Resource Server 校验的外部 OIDC Bearer Token 都走 Auth 中心计费。第一次请求由已验证的 `(iss, sub)` 自动建立 API-only 本地用户和隐藏的内部路由 Key；无需用户登录网关，也无需管理员逐个录入身份。部署时须已有带活跃上游账号的公开标准分组供新用户路由；没有可用分组时请求失败，数据库不会留下半建档用户。内部 Key 持久标记为 `auth_billing_only=true`，且状态为 `auth_billing_only`；旧版网关会拒绝该状态，关闭自动模式也不能回退本地扣费。它仅用于网关分组路由、访问限制、用量记录和去重，不是用户可见 API Key。并发首次请求依靠唯一身份约束收敛到同一用户与 Key，不按邮箱合并不同 `sub`。

此类身份查询 `/v1/usage` 时，响应的 `mode` 为 `auth_billing`，只报告网关记录的用量；Auth 账户余额和预算应从 Auth 获取，网关不会把本地钱包余额显示为可用资金。

网关将当前 Bearer Token 短暂交给 Auth 的 Quote/Reserve 作为 subject proof。Auth 独立验证 Token 和调用应用、确定实际付款账户及 owner epoch，并检查团队成员资格、可用资金和预算。网关冻结 Auth 返回的付款账户及 epoch，在供应商执行前完成预占；用量产生后由 Auth 结算。Auth 中没有该租户的计费账户、余额或预算不足、授权被撤销、中心连接器关闭或中心服务不可用时，请求直接失败，不会退回本地余额/订阅扣费。网关也不相信 Token、Header 或本地用户记录里自报的付款账户。

已显式迁移到 `central` 的旧 route 继续与 Auth Quote 返回的付款账户和 epoch 严格核对。没有中心 workspace route 时，显式处于 `local`、`shadow`、`fenced`、`draining` 或 `frozen` 的个人 route 会拒绝外部 OIDC Bearer 请求。若该租户已有独立登记的中心 workspace route，团队资金与个人旧资金分离，仍需由 Auth Quote 校验团队实际付款账户及成员资格，个人旧余额不能用于本次调用。普通 API Key 继续遵守原有资金权威与迁移流程。关闭 `auto_provision` 后，原有显式绑定行为不变；已经自动生成、带 `auth_billing_only` 标记的 Key 继续 fail closed，不能回退本地扣费。

### 旧有管理员显式绑定

关闭自动模式时，网关只通过经过验证的 `(iss, sub)` 查找预先绑定的内部 API Key。该 Key 决定用户、分组路由、原有余额或订阅、额度、限流和 IP 策略；`tenant` 与 `X-Tabro-*` Header 都不能替代绑定。已有手动绑定可继续使用，但启用自动模式前必须先处理其资金权威：需要个人 `central` route 或独立的中心 workspace route；没有中心 workspace 时，非中心个人 route 会拒绝外部 Bearer。

对于暂未启用自动模式、无需登录网关页面的用户，管理员仍可在用户管理中创建 **API-only** 用户，保持账号状态为 `active`，按旧模式配置分组及资金。API-only 只限制网关网页会话；若把账号状态设为 `disabled`，模型 API 也会被拒绝。

然后从可信 IdP 管理面取得该用户的稳定 `iss` 和 `sub`，调用一次按用户预配接口。用户不需要先访问本网站或完成 `oidc_connect` 登录：

```bash
curl -X PUT 'https://llm.example.com/api/v1/admin/users/<user-id>/oidc-gateway-identity' \
  -H 'Authorization: Bearer <Tabro 管理员登录令牌>' \
  -H 'Content-Type: application/json' \
  --data '{
    "issuer": "https://idp.example.com/realms/production",
    "subject": "<IdP 中稳定且可信的 subject>"
  }'
```

接口创建或复用该用户的内部 OIDC Key，并以不可变方式绑定 `(iss, sub)`；响应中的 `api_key.id` 可用于审计，`api_key.key` 不会暴露内部密钥。相同身份重复提交是幂等的；同一 Key 改绑其他身份、或同一身份已经绑定其他有效 Key，会返回冲突。客户端仍使用专用于网关、带 `llm.invoke` 的 IdP access token；`oidc_connect` 的网页登录令牌不能代替网关 access token。

已有内部 Key 或需要精确控制 Key 分组、额度、限流、IP 规则时，可以继续使用以下按 Key ID 绑定接口。

先在管理后台为目标用户创建或选择一条用于网关计费和路由的 API Key，并取得其数字 ID。管理员也可通过 `GET /api/v1/admin/users/:userId/api-keys` 查找用户的 Key。

然后调用：

```bash
curl -X PUT 'https://llm.example.com/api/v1/admin/api-keys/<key-id>/oidc-identity' \
  -H 'Authorization: Bearer <Tabro 管理员登录令牌>' \
  -H 'Content-Type: application/json' \
  --data '{
    "issuer": "https://idp.example.com/realms/production",
    "subject": "<IdP 中稳定且可信的 subject>"
  }'
```

绑定规则：

- `issuer` 必须是绝对 HTTP(S) URL，并应与 Resource Server 配置及 Token `iss` 完全一致。
- `(issuer, subject)` 在所有未删除 Key 中全局唯一。
- 绑定采用不可变的 compare-and-set：重复提交相同绑定是安全的，但不能把已经绑定的 Key 改绑到另一个身份。
- 也不能把同一身份同时绑定到另一条有效 Key。需要迁移时，应先完成业务停流，删除旧绑定 Key，再把身份绑定到新 Key。
- 身份值应从 IdP 管理面或已受信任验证流程取得，不能相信调用方额外发送的邮箱、用户名或 Header。

### 通过 `oidc_connect` 自动绑定

如果同一用户通过已经启用的 `oidc_connect` 完成首次登录，Tabro 只会在 IdP 明确返回 `email_verified=true` 时按邮箱创建或选择本地用户，然后创建或复用内部 OIDC Key，并把已验证的 `(iss, sub)` 绑定到该 Key。后续登录优先按不可变的 `(iss, sub)` 绑定解析用户，不再用可变邮箱重新选择计费身份。这适合允许进入网关页面的交互式用户。自动 Resource Server 模式创建的是 API-only 用户，无法借这条网页登录路径进入网关后台。

同一个 `(iss, sub)` 只对应一条有效内部 Key。自动模式会先复用已经显式绑定的 Key，并对其资金 route 做中心计费校验；没有绑定时才自动创建。不能把同一身份改绑到另一条仍有效的 Key。

## Tabro Client 请求格式

建议每次逻辑模型调用都发送以下 Header：

```http
Authorization: Bearer <IdP access token，aud 仅为 tabro-llm>
Idempotency-Key: <runId>:<nodeId>:<logicalCallId>
X-Tabro-Run-Id: <runId>
X-Tabro-Project-Id: <projectId>
Content-Type: application/json
```

示例：

```bash
curl 'https://llm.example.com/v1/chat/completions' \
  -H 'Authorization: Bearer <gateway-access-token>' \
  -H 'Idempotency-Key: run-42:node-7:call-1' \
  -H 'X-Tabro-Run-Id: run-42' \
  -H 'X-Tabro-Project-Id: project-9' \
  -H 'Content-Type: application/json' \
  --data '{
    "model": "<model-name>",
    "messages": [{"role": "user", "content": "hello"}]
  }'
```

Header 约束与语义：

- `Idempotency-Key` 最长 128 个 ASCII 可见字符（`!` 到 `~`，不含空格）。同一逻辑调用的网络重试必须复用相同值；不同逻辑调用必须使用不同值。
- 自动中心计费的 operation ID 由已验证的 `iss + sub + tenant + app + Idempotency-Key` 派生；原始幂等键和 Token 不写入持久记录。旧本地计费路径继续以内部 Key 和自己的调用前幂等记录为边界。
- 中心模式在调用模型供应商前持久化 Auth reservation，并用本地状态转换限制供应商执行次数；同一个 operation 换请求内容会冲突，重复完成的调用不能再次执行。
- 已完成请求再次提交时返回 `409` 和 `X-Idempotency-Replayed: true`，不会再次调用供应商或重复扣费。由于模型响应可能是长连接流，网关当前不缓存并重放完整响应体；调用方应把这个返回视为“原逻辑调用已经执行”，并按 run 状态恢复，而不是换一个 Key 盲目重发。
- 用量和结算事件仍使用独立的数据库去重，并由 Auth 对相同 operation 做资金幂等处理。
- 在“供应商已经执行，但网关进程在保存完成状态前崩溃”这类不可判定故障中，不承诺跨供应商的严格 exactly-once；记录会保持占用直到过期，避免立即重复调用。
- Responses WebSocket 是长连接协议，当前不缓存或重放某一轮响应；每个 `response.create` 有独立中心 operation。同一逻辑调用因网络故障重连时，必须复用同一 `Idempotency-Key` 和对应 turn，以避免重复扣费；不同逻辑调用必须使用不同 Key。未提供 `Idempotency-Key` 时，每个 WebSocket 连接都使用随机计费世代，网关无法为跨连接重试提供扣费幂等保证。
- `X-Tabro-Run-Id` 和 `X-Tabro-Project-Id` 最长 128 个可打印 ASCII 字符，只用于用量记录关联。无效值会被忽略。
- 身份来自已验签 Token；自动模式的付款账户由 Auth 决定，不能由这些 Header、网关本地 Key 或可变邮箱决定。

网关根据模型供应商响应中的实际 input/output token 记录用量，并在适配后的响应中返回统一 usage；不以 Tabro Client 自报 token 数作为扣费依据。

自动中心计费中，Auth 的 reservation、资金流水和结算结果是客户扣费权威；网关保存供应商实际用量、operation 和待投递结算事件，用于对账与故障恢复，不改本地用户余额或套餐使用量。旧本地计费路径的 `gateway_usage_ledger` 仍保留原有去重与审计职责。`request_id` 是网关 operation 标识，`upstream_request_id` 是供应商调用 ID，二者不能互相覆盖；`X-Tabro-*` 只作为关联元数据。

中心模式先预占再派发；供应商调用后，网关把用量与结算 outbox 原子保存，由 worker 向 Auth 投递同一事件。Auth 暂时不可用时不得重新用本地余额扣费；保留未完成 operation 和 outbox 以便重试或核对。供应商调用已经发生但网关未保存实际 usage 的故障仍需用供应商 request ID 对账，不能依赖客户端补报 token。

## 上游 OpenAI / Anthropic 凭证

OpenAI、Anthropic 等供应商的 API Key 或上游 OAuth 凭证应配置在网关管理的上游账号中，由网关独立保管和轮换。调用供应商时，网关会根据所选上游账号构造新的认证 Header。

不要：

- 把供应商 API Key 下发到 Tabro Client。
- 把用户的 OIDC Token 配成供应商凭证。
- 在反向代理中把入口 `Authorization` Header 无条件透传到上游。

入口 OAuth Token 与上游供应商凭证属于两个完全不同的信任域，应独立授权、吊销和轮换。

## 错误处理

| HTTP 状态 | 典型原因 | 处理方式 |
|---|---|---|
| `401 Unauthorized` | 签名无效；issuer/audience 不匹配；多 audience；Token 过期或尚未生效；缺少可信 `sub`；Client 不在 allowlist；误用了 Tabro 本地登录令牌 | 不要重试同一 Token；重新申请面向网关的正确 Token |
| `403 Forbidden` | Token 有效但缺少 `llm.invoke` 等必需 Scope | 在 IdP 中授权 Scope 后重新申请 Token |
| `403 Forbidden` | 自动模式关闭时，`(iss, sub)` 尚未绑定到内部 Key | 按旧流程绑定；不要通过 Header 绕过 |
| `403 BILLING_ACCOUNT_NOT_READY` | 自动中心模式下，Auth 没有可计费账户或产品 | 在 Auth 完成账户及产品配置；网关不会使用本地余额 |
| `402/403/409` | Auth 资金/预算不足、成员无权，或旧 route 资金权威冲突 | 检查 Auth 成员授权、余额与迁移状态 |
| `403/429` | 身份已通过，但内部 Key、用户、额度或 IP 策略不允许本次调用 | 按网关访问策略排查 |
| `409 Conflict` | 幂等请求仍在执行、已经完成，或同一 Key 对应了不同请求 | 不要生成新 Key 盲目重试；按 `Retry-After`、`X-Idempotency-Replayed` 和原 run 状态处理 |
| `503 Service Unavailable` | Discovery/JWKS、Auth 中心或幂等存储暂时不可用 | 检查 IdP、Auth、网络和数据库；中心计费不得回退本地扣费 |

缺少 Scope 是授权失败，因此返回 `403`；Token 无效或发给了错误资源是认证失败，因此返回 `401`。不要为了减少 `401` 而放宽 audience、issuer 或签名校验。

## 数据库迁移与上线顺序

升级版本在服务启动时自动执行相关数据库迁移：

- 为 `api_keys` 增加外部身份绑定字段和活动绑定唯一索引。
- 为自动绑定的 Key 增加持久 `auth_billing_only` 标记，确保关闭自动配置或中心连接器后仍拒绝本地扣费。
- 为 `usage_logs` 增加已验证 OIDC 身份、租户及 Tabro 关联字段。
- 新建只追加的 `gateway_usage_ledger`，分开保存网关计费 `request_id` 与供应商 `upstream_request_id`，并以 `(request_id, api_key_id)` 唯一约束和计费事务原子提交。

推荐上线顺序：

1. 备份 PostgreSQL，并先在预发布环境完成升级。
2. 部署新版本但暂时保持 `gateway.resource_server.enabled: false`。现有普通 API Key 调用不受影响。
3. 在 IdP 创建/更新 `tabro-llm` 网关资源与 Scope，并给已有 `tabro-agent` Client 补齐授权，确认 JWT claim 符合本文要求。
4. 在 Auth 配置计费账户、客户余额/团队预算、网关 producer 权限和服务 Client；旧本地资金账户仍按中心计费迁移流程冻结、导入和核对，不因自动绑定而搬钱。
5. 启用 `billing_center` 与 Resource Server，再开启 `auto_provision`。在预发布环境验证首次自动绑定、重试无重复身份、错误 audience、缺少 Scope、过期 Token、Auth 余额不足及中心故障时拒绝调用。
6. 修改 Tabro Client，使其申请网关专属 Token，并为每次逻辑调用发送稳定的 `Idempotency-Key`。
7. 灰度切换生产流量，监控 `401`、`402`、`403`、`503`、用量记录、Auth 预占与结算结果。
8. 所有旧调用方迁移完成后，清理 IdP 中不再需要的授权和旧 Client。

旧版若曾把 Tabro 本地登录 JWT 当作网关 API Key 使用，升级后这类请求会返回 `401`。正确迁移方式是让调用方从 IdP 获取 `aud` 唯一指向 LLM 网关的 access token，而不是把旧 Token 加入 allowlist 或关闭 audience 校验。

旧数据中若存在恰好包含两个 `.` 的普通 API Key，也必须在升级前轮换；网关会把这种外形视为 JWT，并以 fail-closed 方式拒绝，不再回退查询 API Key。

## 日志、隐私与密钥轮换

### 日志与隐私

- 网关调试日志会完全跳过 `Authorization`、`Proxy-Authorization`、Cookie、API Key Header 和 `Idempotency-Key`，连 Header 名也不写入；原始 Token 和原始幂等键不会进入应用日志。
- 用量记录可保存已验证的 issuer、subject、tenant，以及 `X-Tabro-Run-Id`、`X-Tabro-Project-Id`。这些字段仍可能属于敏感标识，应设置适当的访问控制和保留周期。
- 同时检查 Nginx、Ingress、WAF、APM 和链路追踪配置，确认它们不会记录入口 Authorization、Cookie、API Key 或完整请求体。
- 排障时不要在工单、聊天或截图中粘贴完整 Token；只记录 request ID、时间、issuer、脱敏后的 subject 和错误码。

### IdP 签名密钥轮换

- 新密钥投入签发前先发布到 JWKS，并使用新的唯一 `kid`。
- 旧密钥应至少保留到所有旧 Token 过期，再加上允许的时钟偏差；不要在仍有有效 Token 时从 JWKS 删除。
- 网关按 `jwks_cache_ttl_seconds` 缓存 JWKS，遇到未知 `kid` 会触发刷新。仍建议保留新旧密钥重叠窗口，避免多实例或网络故障造成瞬时失败。

### Client 与上游密钥轮换

- Tabro OAuth Client ID 变化时，可短期把新旧 ID 同时加入 `allowed_client_ids`；旧 Token 全部过期后移除旧 ID。
- Client Secret 只影响 Tabro 向 IdP 取 Token，不应配置到网关 Resource Server 中。
- OpenAI/Anthropic 等上游密钥单独轮换，先验证新凭证，再撤销旧凭证；该过程不应改变 gateway audience 或 OIDC 身份绑定。
- issuer 或 audience 的改变相当于安全域迁移。当前配置固定一个 issuer 和一个 audience，建议通过新入口/新实例做并行迁移，不要临时接受多个 audience。

## 上线检查清单

- [ ] `oidc_connect` 与 `gateway.resource_server` 的用途、Client 和 audience 已明确区分。
- [ ] Token `iss` 与配置完全一致，`aud` 只有 `tabro-llm`。
- [ ] Token 有 `exp`、可信 `sub`、`llm.invoke`，以及 allowlist 中的顶层字符串 `azp`；`client_id` 没有被当作替代值。
- [ ] 使用 Token Exchange 时，每层 `act.sub` 都存在且在 actor allowlist 中，immediate `act.sub` 与顶层 `azp` 完全一致。
- [ ] 开启自动模式时，首次有效 Bearer Token 建立唯一 API-only 路由身份；没有逐用户管理员绑定步骤。
- [ ] Auth Quote 决定付款账户与 owner epoch；无账户/余额/预算或 Auth 故障会拒绝，网关不作本地扣费回退。
- [ ] 既有显式 `central` route 与 Auth Quote 严格一致；无中心 workspace 时，非中心个人 route 拒绝外部 Bearer。
- [ ] Tabro Client 在网络重试时复用同一 `Idempotency-Key`。
- [ ] `X-Tabro-*` 只用于关联，没有参与身份或计费归属判断。
- [ ] 网关 operation/用量与 Auth reservation/结算能按 operation ID 对账。
- [ ] 已对计费事务最终失败设置告警，并准备按供应商 request ID 做人工或自动对账。
- [ ] 网关使用自己的上游供应商凭证，入口 OIDC Token 没有被透传。
- [ ] 应用、反向代理、APM 和调试日志均不会记录 Token、Authorization 或原始幂等键。
- [ ] JWKS 新旧密钥重叠、Token TTL、Client 与上游密钥轮换流程已经演练。
