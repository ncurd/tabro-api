# LLM 网关 OIDC Resource Server 配置与迁移指南

本文说明如何让 Tabro 的 LLM 网关以真正的 OAuth 2.0 Resource Server 方式接收访问令牌，并将已经验证的外部身份映射到 Tabro 内部的路由与计费 Key。

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

## 调用链路

```text
Tabro Client ──向 IdP 申请 access token──> IdP
      │
      │ Authorization: Bearer <aud=tabro-llm>
      ▼
LLM 网关
  1. 验证签名、iss、唯一 aud、exp/nbf、scope、Client 和 act
  2. 用已验证的 (iss, sub) 查找内部计费/路由 Key
  3. 选择网关中配置的上游账号
      │
      │ 使用网关自己保管的 OpenAI/Anthropic 等上游凭证
      ▼
模型供应商 ──返回实际 usage──> 网关计量、幂等扣费并返回统一 usage
```

入口 Bearer Token 只证明“谁有权使用网关、费用记到谁”。网关不会把它直接转发给 OpenAI、Anthropic 或其他模型供应商。

## 网关验证的 Token 条件

网关采用 fail-closed 策略。一个 Token 必须同时满足以下条件：

- JWT 签名可由配置的 JWKS 验证，且算法属于 `RS256`、`ES256`、`PS256` 中显式允许的算法。
- Discovery/JWKS 地址由固定配置或可信 Discovery 决定；HTTPS issuer 的每一跳重定向都禁止降级到 HTTP。
- `iss` 与 `gateway.resource_server.issuer_url` 完全一致。
- `aud` 只有一个值，且该值与 `gateway.resource_server.audience` 完全一致。
- 必须存在有效的 `exp`；如果存在 `nbf`，也必须已经生效。仅允许配置范围内的时钟偏差。
- `scope` 或 `scp` 包含所有 `required_scopes`，默认要求 `llm.invoke`。Scope 按完整词匹配，不做子串匹配。
- 顶层 `azp` 必须是非空字符串且位于 `allowed_client_ids` 中；`client_id` 不能替代它，若存在则必须与 `azp` 相同。
- `sub` 必须存在且非空，并且 `(iss, sub)` 已显式绑定到一条有效的 Tabro 内部 API Key。
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
- 每一层 `act` 都必须用非空字符串 `sub` 标识 actor；`client_id` 或 `azp` 不能替代 `sub`。嵌套委托可继续包含 `act`。
- 同一层 `act` 若额外携带 `client_id` 或 `azp`，它们必须是非空字符串并与该层 `sub` 完全一致；任一字段类型错误、为空或值冲突都会使整个 Token 被拒绝。
- 调用应用必须同时加入 `allowed_client_ids`；Token Exchange 中的每一层 actor ID 还必须加入 `allowed_actor_client_ids`。
- 只要 Token 携带 `act`，actor allowlist 就不能留空；留空会拒绝该 Token。
- 仅接受 Token Exchange 的部署建议设置 `require_actor: true`，强制所有网关 Token 都携带 `act`。如果还要接受没有 `act` 的 `client_credentials` 服务 Token，则必须保持 `false`；但只要 Token 实际携带 `act`，无论该开关为何值，actor 仍会严格校验且空 allowlist 仍会拒绝。
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

    # 可选的可信租户 claim
    tenant_claim: "tenant_id"
    require_tenant: false

    token_exchange:
      # Agent/Drama 的 Token Exchange 部署必须为 true；仅在明确还要接受
      # client_credentials Token 时才可按独立风险评估改为 false
      require_actor: true
      actor_claim: "act"
      # 显式 allowlist；留空会拒绝所有携带 act 的 Token
      allowed_actor_client_ids: "tabro-agent"
      max_delegation_depth: 4
```

示例保留 `require_tenant: false` 以兼容单租户 IdP。若上线要求每条计费账本都必须记录 `sub + tenant`，应改为 `true`，并把 `tenant_claim` 设置成 IdP 实际签发且始终非空的 claim 名；否则 tenant 只会在 Token 提供时记录。

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

如果确实需要独立的 `client_credentials` 服务身份模式，应另用 `tabro-model-service`，在 Agent 设置页面填写并由服务端加密保存它自己的 Client Secret；同时把该 Client 加入 `allowed_client_ids`，并保持 `require_actor: false` 以接受不带 `act` 的服务 Token。它不是当前 `tabro-agent` 的用户委托模式，也不要因为它没有 `act` 就把它加入 actor allowlist。

认证模式的选择会直接影响计费身份：

- `oauth-client-credentials` 的 `sub` 是服务身份，只适合把费用统一记到 Tabro 服务账号。
- 要按最终用户计费，应使用 `oauth-token-exchange`：交换后的 Token 保留可信用户 `sub`，`aud` 为 `tabro-llm`，并以 `azp` 和可验证的 `act` 委托链标识调用应用。用户登录 Token 只交给受信任的 Token Broker，不能直接发送给 LLM 网关或模型供应商。

已有安装必须通过 Agent 的 Model Provider 设置或随版本提供的离线安装配置迁移更新，不能只改 Compose。切流前应检查安装文件、页面显示的非秘密配置和最终签发 Token 是否一致。

## 将外部身份绑定到内部计费 Key

网关只通过经过验证的 `(iss, sub)` 查找内部 API Key。内部 Key 决定用户、分组路由、余额或订阅、额度、限流和 IP 策略；`tenant` 与 `X-Tabro-*` Header 都不能替代该绑定。

### 管理员显式绑定

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

如果同一用户通过已经启用的 `oidc_connect` 完成首次登录，Tabro 只会在 IdP 明确返回 `email_verified=true` 时按邮箱创建或选择本地用户，然后创建或复用内部 OIDC 计费 Key，并把已验证的 `(iss, sub)` 绑定到该 Key。后续登录优先按不可变的 `(iss, sub)` 绑定解析用户，不再用可变邮箱重新选择计费身份。这适合交互式用户；服务账号、不能提供已验证邮箱、需要指定分组的身份或批量迁移更适合使用管理员显式绑定 API。

同一个 `(iss, sub)` 只选择一种初始化方式。若已手工绑定到另一条 Key，再用同一身份触发自动绑定，会因唯一性保护而失败。

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

- `Idempotency-Key` 最长 128 个 ASCII 可见字符（`!` 到 `~`，不含空格）。相同内部计费 Key 下，同一逻辑调用的网络重试必须复用相同值；不同逻辑调用必须使用不同值。
- 网关只保存由内部 Key ID 和 `Idempotency-Key` 计算出的哈希计费 request ID，不把原始幂等键写入请求上下文或调试日志。
- 网关在调用模型供应商前持久化占用该 Key，并校验请求路径、查询参数和请求体指纹；同 Key 不同请求返回 `409`，并发中的相同请求也返回 `409` 和 `Retry-After`，均不会触达上游。
- 已完成请求再次提交时返回 `409` 和 `X-Idempotency-Replayed: true`，不会再次调用供应商或重复扣费。由于模型响应可能是长连接流，网关当前不缓存并重放完整响应体；调用方应把这个返回视为“原逻辑调用已经执行”，并按 run 状态恢复，而不是换一个 Key 盲目重发。
- 账单写入仍使用独立的数据库原子去重，作为调用前幂等占用之后的第二道保护。
- 在“供应商已经执行，但网关进程在保存完成状态前崩溃”这类不可判定故障中，不承诺跨供应商的严格 exactly-once；记录会保持占用直到过期，避免立即重复调用。
- 上述“调用前占用、重复请求返回 `409`”的保护适用于 `POST` 模型请求。Responses WebSocket 是长连接协议，当前不缓存或重放某一轮响应；但会用已验证计费身份下的 `Idempotency-Key` 和稳定 turn 序号派生计费 request ID。同一逻辑调用因网络故障重连时，必须复用同一 `Idempotency-Key` 和对应 turn，以避免重复扣费；不同逻辑调用必须使用不同 Key。未提供 `Idempotency-Key` 时，每个 WebSocket 连接都使用随机计费世代，网关无法为跨连接重试提供扣费幂等保证。
- `X-Tabro-Run-Id` 和 `X-Tabro-Project-Id` 最长 128 个可打印 ASCII 字符，只用于用量记录关联。无效值会被忽略。
- 用户身份和计费归属只来自已经验签的 Token 及 `(iss, sub)` 绑定，不能由这些 Header 单独决定。

网关根据模型供应商响应中的实际 input/output token 记录用量，并在适配后的响应中返回统一 usage；不以 Tabro Client 自报 token 数作为扣费依据。

`gateway_usage_ledger` 是权威、只追加的计费审计账本，和计费去重声明、余额/订阅/额度扣减在同一个数据库事务中提交；任一账本写入失败都会回滚本次扣费。它记录 `sub + tenant + model + request_id`、独立的供应商 `upstream_request_id`、请求模型与实际上游模型、实际 token、成本和扣费 effect。`request_id` 是稳定的网关计费/去重 ID，`upstream_request_id` 是 OpenAI、Anthropic 等供应商返回的调用 ID，二者不能互相覆盖；后者有独立的部分索引，便于按供应商账单快速对账。`usage_logs` 保存更详细的查询/分析信息，但属于独立的 best-effort 记录，不能作为唯一财务账本。`X-Tabro-*` 即使写入两者，也始终只是关联元数据。

计费事务遇到 PostgreSQL 连接、序列化或死锁等瞬时故障时会做有界重试；同一 `request_id + api_key_id + fingerprint` 的事务重试仍由数据库去重，因此不会重复扣费。计费关键任务默认有 30 秒总时间预算，单次数据库阶段另有独立上限；不要把任务预算改回小于定价解析和事务重试所需的时间。若所有重试均失败，账本、去重声明和扣费 effect 会一起回滚并产生错误日志。供应商调用已经发生但网关在保存实际 usage 前发生不可恢复的数据库故障或进程崩溃，是跨系统无法原子提交的剩余故障窗口；生产环境必须对此类计费失败告警，并用供应商 request ID 与 `gateway_usage_ledger` 做对账，不能依赖客户端补报 token。

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
| `403 Forbidden` | `(iss, sub)` 尚未绑定到内部计费 Key | 由管理员完成显式绑定；不要通过 Header 绕过 |
| `403/429` | 身份已通过，但内部 Key、用户、订阅、余额、额度或 IP 策略不允许本次调用 | 按 Tabro 业务策略排查 |
| `409 Conflict` | 幂等请求仍在执行、已经完成，或同一 Key 对应了不同请求 | 不要生成新 Key 盲目重试；按 `Retry-After`、`X-Idempotency-Replayed` 和原 run 状态处理 |
| `503 Service Unavailable` | Discovery/JWKS 或幂等存储暂时不可用，网关无法安全验签/占用调用 | 检查 IdP、DNS、TLS、网络、JWKS 和数据库状态；不要降级关闭验签或幂等保护 |

缺少 Scope 是授权失败，因此返回 `403`；Token 无效或发给了错误资源是认证失败，因此返回 `401`。不要为了减少 `401` 而放宽 audience、issuer 或签名校验。

## 数据库迁移与上线顺序

升级版本在服务启动时自动执行相关数据库迁移：

- 为 `api_keys` 增加外部身份绑定字段和活动绑定唯一索引。
- 为 `usage_logs` 增加已验证 OIDC 身份、租户及 Tabro 关联字段。
- 新建只追加的 `gateway_usage_ledger`，分开保存网关计费 `request_id` 与供应商 `upstream_request_id`，并以 `(request_id, api_key_id)` 唯一约束和计费事务原子提交。

推荐上线顺序：

1. 备份 PostgreSQL，并先在预发布环境完成升级。
2. 部署新版本但暂时保持 `gateway.resource_server.enabled: false`。现有普通 API Key 调用不受影响。
3. 在 IdP 创建/更新 `tabro-llm` 网关资源与 Scope，并给已有 `tabro-agent` Client 补齐授权，确认 JWT claim 符合本文要求。
4. 为计划迁移的每个 `(iss, sub)` 创建或选择内部计费 Key，并完成绑定。
5. 配置 Resource Server 并开启，在预发布环境分别验证成功、错误 audience、缺少 Scope、过期 Token 和未绑定身份。
6. 修改 Tabro Client，使其申请网关专属 Token，并为每次逻辑调用发送稳定的 `Idempotency-Key`。
7. 灰度切换生产流量，监控 `401`、`403`、`503`、用量记录和扣费去重结果。
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
- [ ] 每个 `(iss, sub)` 已绑定到正确的内部计费/路由 Key。
- [ ] Tabro Client 在网络重试时复用同一 `Idempotency-Key`。
- [ ] `X-Tabro-*` 只用于关联，没有参与身份或计费归属判断。
- [ ] `gateway_usage_ledger` 与实际余额/订阅扣费能按 request ID 对账。
- [ ] 已对计费事务最终失败设置告警，并准备按供应商 request ID 做人工或自动对账。
- [ ] 网关使用自己的上游供应商凭证，入口 OIDC Token 没有被透传。
- [ ] 应用、反向代理、APM 和调试日志均不会记录 Token、Authorization 或原始幂等键。
- [ ] JWKS 新旧密钥重叠、Token TTL、Client 与上游密钥轮换流程已经演练。
