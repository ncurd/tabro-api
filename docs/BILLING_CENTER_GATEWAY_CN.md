# Go 网关中心计费实施说明

本文件说明已实现的网关接入。总体资金切换流程见 [主集成方案](BILLING_CENTER_INTEGRATION_CN.md)，Auth 的资金与账户规则见 `tabro-auth/docs/UNIFIED_BILLING_PLAN_CN.md`。

## 启用与资金权威

`billing_center.enabled` 启用 Auth 计费连接器。后台「系统设置 → OIDC 登录 → Auth 计费连接配置」保存的数据库配置优先于部署 YAML 中的 `billing_center` 与 `gateway.resource_server` 初始配置，对新请求和计费 worker 动态生效；后台不配置 `billing_center.payments`，历史支付适配器仍使用独立的部署配置与凭据。旧有本地用户是否已经迁移到中心，仍由数据库 route 决定；启用连接器不会自动搬钱或建立邮箱映射。配置关闭时，已经迁移的账户仍拒绝收费请求，不能转回本地余额。`draining`、`frozen`、`fenced` 拒绝新收费工作。

### 后台简化接入 tabro-auth

先在 Auth 完成一次部署配置：启用 `OpenIddict:Tabro:Billing:Enabled`，登记独立的 `tabro-api-gateway` producer，为其设置专用密钥、`credit_amount` meter，以及 `billing.reserve billing.dispatch billing.extend billing.settle billing.release billing.read` 权限。`AllowedAppIds` 应包含实际模型调用应用（默认 `tabro-agent`）；需要旧 API Key 接入时另包含 `legacy-api-key`。保留 Auth 已有的其他 producer 和应用授权。仅在 Auth 客户端管理中创建普通 OIDC 客户端，不能替代这项 producer 配置。具体模板见 `tabro-auth/docs/examples/billing-config/auth-cnprod.billing.json.template`。

API 管理后台按以下顺序操作：

1. 保存 OIDC 登录配置并开启「仅 OIDC」。已有登录客户端 `tabro-llm` 及其登录密钥保持原配置。
2. 在「Auth 计费连接配置」填写 `tabro-api-gateway` 的专用计费密钥，点击「验证并配置」。服务地址直接使用已保存的 OIDC issuer；未保存的页面输入不参与验证。已有同一 issuer、同一 producer 的密钥可留空保留。
3. 验证成功后，手动打开全局「启用 OIDC 计费」，设置计费倍数和每日结算时间，再保存系统设置。默认倍数为 `1`，每天北京时间 `00:00` 结算。

自动配置先读取已保存 issuer 的 OIDC discovery，核对返回的 issuer 完全一致，并要求 discovery 和 Token 地址使用同源 HTTPS，拒绝重定向；随后通过专用计费凭据申请包含上述六项权限的服务 Token。只有检查成功才保存连接配置，不保存或回显 Token，也不修改登录客户端或自动打开全局计费开关。该检查验证连接及服务凭据，不代替实际请求的 Auth 账户、资金和 Quote/Reserve 授权。

高级参数默认折叠。自动配置使用 `audience=tabro-llm`、`llm.invoke`、必需的 `tenant_id` 与 `act`，委托深度为 `1`，不会通过关闭租户或 Actor 校验来简化接入。模型应用和 Actor 白名单默认均为 `tabro-agent`；同一 issuer 已有的非空白名单会保留。网页登录客户端 ID 不会自动加入这些白名单；增加调用应用时须同时在 Auth 授权。

普通 API Key 仍需在 Auth 登记 credential binding 并配置中心资金路由。仅完成 OIDC 网页登录、按邮箱找到同一用户或打开计费开关，都不能替代该登记。无需逐用户登记的自动模式使用 Auth 签发、经过网关校验的模型 access token；网页登录 JWT 或 ID token 不能冒充模型调用凭据。

专用密钥轮换时先同步 Auth 的 producer 凭据与部署配置，再在同一计费身份下重新验证保存；runtime 会使用新配置，已预扣调用继续沿用冻结责任与金额。切换 producer、Auth 服务地址或 issuer，则须先停用全局计费并完成待结账操作，再关闭连接器并切换；有未结 operation 时服务拒绝关闭或改换计费身份。保存与准入的并发保护目前在单 API 实例内生效，多实例部署切换身份还须协调所有实例的收费准入，不能依赖单个实例的锁。

### 仅 OIDC 模式下的每日计费

后台「系统设置 → OIDC 登录」提供 OIDC 计费开关。先保存并验证仅 OIDC 登录模式；通过上述简化接入或高级配置启用有效的 `billing_center` 连接器和 `gateway.resource_server`，且网页登录与网关使用同一个 issuer，才允许开启。`oidc_billing_supported` 表示当前生效的连接配置具备接入能力，不是 OIDC 标准声明，也不是持续的 Auth 在线健康检查；实际调用仍必须通过 Auth 的 Quote、Reserve、Dispatch 授权。

| 设置 | 默认值 | 作用 |
|---|---|---|
| `oidc_billing_enabled` | `false` | 开启全站 Auth 计费与每日结算 |
| `oidc_billing_rate_multiplier` | `1` | 正数；实际积分 = 网关模型/媒体费用 × 分组或用户倍率 × OIDC 计费倍数 |
| `oidc_billing_settlement_time` | `00:00` | 每日结算时间，24 小时制 `HH:MM` |
| `oidc_billing_settlement_timezone` | `Asia/Shanghai` | 明确的 IANA 时区；默认北京时间 |

开启后，验证通过的外部 OIDC Bearer 可自动建立内部路由身份，无需另开 `auto_provision`。Auth 负责资金账户与可用积分；网关按每次调用的真实用量和冻结价目计算明细。普通 API Key 必须已有 Auth credential binding 和中心资金路由，否则拒绝调用；不能从旧积分、套餐或券中补扣。旧账户的显式资金隔离状态仍须按迁移流程处理，开关不迁移或清空历史资金。

调用前先预扣锁定执行上界所需积分，成功 Dispatch 后才调用上游。完成时将真实费用、价格快照和结算事件持久化，到准入之后的第一个每日结算时间再由 worker 逐笔转为实际账单扣款。金额不足或 Auth 不可用时不执行上游工作。**预扣上界在日内保持锁定，到实际结算时扣除真实金额并释放余量**；Auth 当前没有单独缩减预扣的接口。尚未执行上游的失败调用立即排队释放，已执行但用量不明的调用保留待核对。

结算截止时间和计费快照保存在 operation 中。调整倍数、时间、时区或关闭开关，仅影响后续新请求，不重算已预扣调用；每日 worker 不依赖网页会话，也不保存用户 Token。服务停机错过结算时间，恢复后补结算；投递超时沿用原事件 ID 和内容重试。跨结算点仍在执行的调用，在取得完整真实用量后立即补入到期队列。多副本通过数据库租约领取事件，正常空闲轮询间隔为 worker 配置值，不保证所有账单恰在同一秒完成。

本地余额充值、余额修改、套餐授权、优惠码及兑换券 API 同时关闭，界面隐藏相应入口，新 OIDC 用户也不再获得默认本地余额或套餐。历史订单与签名支付回调保留，用于处理切换前的订单；已存在的 Auth 账户中心链接可继续使用。部署级 `auto_provision` 和已迁移中心账户的原有保护规则不受此开关替代，关闭开关不会让 Auth-only 身份回退本地扣费。

升级须执行 `129_billing_center_daily_settlement.sql`，此前历史 operation 的空截止时间保留即时结算行为。数据库中的 `settlement_pending` 表示已记录真实费用、等待中心结算，不能把它显示为已实际扣款。每日结算沿用现有逐笔账单接口，不合并不同调用的幂等身份。

### 外部 OIDC Bearer 自动接入

同时启用 `gateway.resource_server.enabled`、`gateway.resource_server.auto_provision` 和 `billing_center.enabled` 后，所有有效的外部 OIDC Bearer 调用采用中心计费。网关按已验签的 `(iss, sub)` 首次自动建立 API-only 本地用户和隐藏路由 Key；管理员无需为每个用户手动配置绑定。部署时须已有带活跃上游账号的公开标准分组；否则首访失败且不会留下半建档身份。自动生成的 Key 持久标记 `auth_billing_only=true`，状态也为 `auth_billing_only`，以便旧版网关拒绝它；本地身份仅用于路由、权限和用量外键，不成为客户资金权威。不同 `sub` 不通过邮箱合并。已存在的显式绑定仍可使用：个人显式 `central` route 必须与 Auth 返回的付款账户及 epoch 一致；无中心 workspace route 时，显式非 `central` 个人 route 会拒绝 Bearer。已登记中心 workspace 的团队资金独立于该用户个人旧资金，仍需 Auth Quote 验证实际 payer 和成员资格。

本模式中的付款账户由 Auth Quote 根据 subject proof 的用户、租户、应用和实时成员权限选择。网关不采信 Token 或 Header 自报的 payer；Quote 返回的 `billing_account_id`、`owner_epoch` 被用于同一 operation 的 Reserve，随后 Dispatch 和 Settle 由 Auth 执行资金约束。Auth 没有计费账户、资金/团队预算不足、成员失权或 Auth 不可用时，请求 fail closed。关闭 `auto_provision` 或 `billing_center.enabled` 后，已标记 `auth_billing_only` 的 Key 仍不能回退本地余额。普通 API Key 的原有迁移隔离继续生效。

自动接入不等于旧资金迁移。已有本地余额、充值、套餐及未结消费要按 [主集成方案](BILLING_CENTER_INTEGRATION_CN.md) 的冻结、导入、对账和 epoch 流程迁移；不能通过打开自动开关使旧账户变成中心账户。Auth 侧须预先存在相应个人或团队计费账户及可用资金，统一积分产品可在该账户首次可信 Quote 时补齐，账户本身不会由网关凭 Token 创建。

- `119_billing_center_account_routes.sql`：旧个人账户资金权威，固定 actor、issuer、tenant、account、owner epoch。
- `121_billing_center_workspace_routes.sql`：显式 workspace issuer/tenant → 中心 account/epoch。已验证成员仍是 actor，多团队使用不同付款账户。
- `122_billing_center_credential_bindings.sql`：每个本地 API Key 对应独立的中心 binding ID/version；不保存原始 Key。中心核实登记身份及总额/滚动窗口限额。
- 自动模式的 Auth Bearer 以短暂 subject proof 由 Auth 解析 payer，无需为每个用户创建上述旧资金 route 或旧 Key credential binding；这些表仍保护已有迁移账户与普通 API Key。
- 请求来源 app 来自已验证 principal；已登记旧 Key 固定为 `legacy-api-key`，不接受调用者自报 actor 或 payer。

发布时须应用 `118` 至 `126` 的全部迁移，不能只创建账户映射表；`126` 为运维恢复 worker 所需的管理员决定回执和 outbox 状态扩展。

客户端凭据权限为 `billing.reserve billing.dispatch billing.extend billing.settle billing.release billing.read`。用户 subject proof 仅随 Quote/Reserve/Extend 发送，不能持久化到 operation、job、outbox 或日志。恢复结算只用服务身份与冻结 reservation。

## 真实请求链路

HTTP 文本、Gemini 原生、WebSocket Responses、图片生成/编辑（JSON/multipart）、视频、Qwen ASR/克隆/TTS、Azure 同步/批量语音已接入。文本目录未知且无法验证输入/输出上限的模型拒绝中心执行；不能使用模糊价格匹配推测能力。

文本输入按已验证的模型完整输入上限预占，OpenAI/Codex 输出按模型完整输出上限预占；其他文本请求使用实际可执行的输出限额。预占可能高于本次实际消费，结算只收取真实用量并释放余量，部署预算需容纳这一执行上界。

1. 网关按已验证的分组、渠道模型映射、用户倍率和现有价目计算积分最大额，冻结价格快照。自动模式下，Quote 由 Auth 的 subject proof 确认真实用户/租户并选择付款账户、owner epoch、资金来源与 `gateway:credits` 技术价格版本；中心不计算模型或媒体价格。
2. 本地 operation intent 持久化后 Reserve；超时查询原 operation，内容冲突拒绝。
3. 本地 CAS 获得一次执行权，再确认中心 Dispatch，最后才调用供应商。HTTP 重定向和隐式 POST 重试不能再次发起收费工作。
4. 真实用量、供应商额度效果、证据账本和 Settle outbox 在一个本地事务中保存。中心模式不改用户旧余额、套餐 used 或客户 Key 金额计数。
5. Worker 用数据库租约投递同一不可变事件。中心提交、本地确认丢失可重放；已投递事件的 expected_version 不随查询到的新版本变化。

WebSocket 每次 `response.create` 都有独立 operation。执行许可在每次实际 frame write 前检查，包括内部恢复尝试。后续轮次被拒绝时，已经发出的轮次仍继续读取到结束或对账超时，以保存能获得的实际用量。

异步媒体在提交前保存任务身份、冻结报价和责任快照。视频任务/批量语音由数据库租约轮询；Azure 的上游任务 ID 在 PUT 前保存，响应丢失后仍可查原任务。任务完成后 `usage_recorded_at` 表示本地用量/事件已提交，中心资金状态另见 `billing_center_operations`，不能把它等同为中心已扣款。

上游调用后崩溃、缺 usage、失败但可能产生费用等状态保留为待核对，不盲目重发供应商，也不根据 HTTP 失败自动释放资金。尚未 Dispatch 的请求才可自动排队 Release。

## 产品与用量契约

中心模式向 Auth 发送唯一 `product_key=gateway:credits`、`service_tier=default` 和 `credit_amount`（通用积分）。网关保留下面各业务 meter 的请求上界与真实用量作证据，并把实际积分金额作为唯一结算 meter。所有金额和数量均为十进制字符串；最大额和实际额都向上取到 10 位小数，显式免费价走零金额预留/结算，缺价或无上界则拒绝执行。管理员调整价目只影响下一请求；当前请求、流式轮次和异步媒体作业使用冻结快照。

下表中的产品和 meter 是网关内部的业务身份与用量证据；中心资金路由只需上述通用积分产品。

| 工作 | product_key | service_tier / meters |
|---|---|---|
| 文本 / Gemini | `ai:<model>` | default/priority/flex/anthropic_fast；input/output、cache read/write TTL、image output、request/image count |
| 视频 | `ai:<model>` | 大写分辨率如 `720P`；`video_seconds`、`request_count` |
| ASR | `ai:<model>` | default；`audio_seconds`、`request_count` |
| TTS | `ai:<model>` | default；`audio_characters`、`request_count` |
| 声音克隆登记 | `ai:qwen-voice-enrollment` | default；`request_count` |
| 图片生成/编辑 | `ai:<model>:images` | 小写 `<size>:<quality>`，省略项为 `auto`；`image_count`、`request_count` |

图片按请求张数预留，按冻结的渠道单价或分组/目录图片单价结算。编辑请求的内容指纹包含原始上传字节，而账单快照不保存文件。渠道档位、分组尺寸价格和目录图片价格都未配置时拒绝执行；显式配置为 0 的单价才表示免费。

缓存计量互斥：使用 5m/1h 明细时不再重复计入 aggregate cache writes；图像输出从总 output 中剔除后单独计量。媒体实际时长保留十进制精度，不用用户请求的 duration 代替上游真实值。

目前以下情况仍需专门计量契约，中心路径会明确拒绝，shadow 不改变原请求：Responses `/compact`、后台 Responses、未单独计量的 hosted tools、无法验证上限的未知模型/自动视频规格、无法在预留前确定价格的上游模型定价，以及 Gemini 生成图片。不能把这些拒绝解释为已完整支持。文本中的图片输入可按模型完整输入上限预占；文件、视频及专用音频 token 输入尚需补充对应可验证上限与计价。已登记的旧 API Key 还必须在 Auth 审核其绑定产品包含 `gateway:credits`，否则中心明确拒绝，不能绕过绑定限制。

## Shadow 与运维

Shadow 调 Quote/Estimate、不预占也不扣中心资金，本地原计费继续执行。报价失败记录 `quote_failed`，实际用量不足记录 `usage_incomplete`，估价失败记录 `estimate_failed` 并重试；不能把失败记录成价差零。

关注：`billing_center_operations` 的 `dispatching`/`reconciliation_required`，`billing_center_outbox` 的 `blocked`/过期租约，媒体 pending jobs，shadow 失败表及尚无 central estimate 的 observation。禁止通过删除去重记录、改付款账户或重放供应商来“修复”不确定操作。

管理员在 Auth 对异常消费作出 `verified_usage` 或 `waive` 决定后，网关 worker 只读查询对应 reconciliation。它核对冻结 actor、tenant、producer/app、payer、balance、allocation、price、unit、product、冻结 period IDs 和 owner epoch，并要求已解决 case、管理员身份和有效 SHA256 决定指纹。迁移 `126` 在本地事务中保存 case/决定完整快照，将原 outbox 标记 `superseded` 并更新最终 operation；原事件 ID、payload 和指纹保持不变。显式免单不会伪装成旧 Settle 已成功投递，尚未得到有效决定的异常工作继续保留。

## 仅内部运维部署

```yaml
deployment:
  internal_only: true
  account_center_url: https://auth.example.com/Identity/Account/Manage/Billing
```

默认 `false` 保持旧部署兼容。启用后，服务端关闭用户注册、用户 Key/钱包/套餐/兑换和本地支付自助 API，以及旧后台资金变更；返回 `410 ACCOUNT_CENTER_REQUIRED` 和账户中心地址。用户控制台跳转 Auth，不转发原 query/token。已有用户 JWT 也不能继续访问自助资金入口。

管理员本地登录、OIDC 管理员登录、供应商账户/配额/渠道运维、管理员自身密码/TOTP、支付提供商配置、PSP 验签 webhook 及模型网关保留。强制后台模式禁止普通用户登录，不能从数据库 settings 开关重新打开注册。管理员历史资金查询仍可审计，实际资金变更在中心执行。

该模式不代替账户资金迁移/epoch fencing。用户曾经的订单、媒体责任须先按主方案处理；保留 PSP webhook 是为了正确接收已登记支付回执，而不是允许旧账户重新入账。

## 验证入口

Go 定向单元/race 测试覆盖 client/token、一次性 dispatch、WS 多轮、media 恢复、精确 meters、模式隔离和内部部署路由。真实 PostgreSQL 用 `billing_integration` tag；连接必须指向显式隔离测试库 `billing_center_test`，测试只创建/删除自己的 schema。另 `billing_auth_integration` tag 读取短期 HTTP fixture 描述文件，联测真实 OpenIddict client_credentials + Kestrel Billing HTTP，fixture 凭据不进入仓库。
