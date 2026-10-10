# Go 网关中心计费实施说明

本文件说明已实现的网关接入。总体资金切换流程见 [主集成方案](BILLING_CENTER_INTEGRATION_CN.md)，Auth 的资金与账户规则见 `tabro-auth/docs/UNIFIED_BILLING_PLAN_CN.md`。

## 启用与资金权威

`billing_center.enabled` 启用 Auth 计费连接器。后台「系统设置 → OIDC 登录 → Auth 计费连接配置」保存的数据库配置优先于部署 YAML 中的 `billing_center` 与 `gateway.resource_server` 初始配置，对新请求和计费 worker 动态生效；后台不配置 `billing_center.payments`，历史支付适配器仍使用独立的部署配置与凭据。旧有本地用户是否已经迁移到中心，仍由数据库 route 决定；启用连接器不会自动搬钱或建立邮箱映射。配置关闭时，已经迁移的账户仍拒绝收费请求，不能转回本地余额。`draining`、`frozen`、`fenced` 拒绝新收费工作。

### 后台简化接入 tabro-auth

先在 Auth 完成一次部署配置：启用 `OpenIddict:Tabro:Billing:Enabled`，登记独立的 `tabro-api-gateway` producer，为其设置专用密钥、`credit_amount` meter，以及 `billing.reserve billing.dispatch billing.extend billing.settle billing.release billing.read` 权限。`AllowedAppIds` 应包含实际模型调用应用（默认 `tabro-agent`）；需要旧 API Key 接入时另包含 `legacy-api-key`。保留 Auth 已有的其他 producer 和应用授权。仅在 Auth 客户端管理中创建普通 OIDC 客户端，不能替代这项 producer 配置。具体模板见 `tabro-auth/docs/examples/billing-config/auth-cnprod.billing.json.template`。

每个 producer 的 `AllowedAppIds` 必须属于 `OpenIddict:Tabro:AllowedClientIds`，只有 `legacy-api-key` 作为显式旧 Key 绑定可例外。例如允许 `tabro-drama` 提交消费明细时，须同时将它加入顶层 `AllowedClientIds`；该列表仍须保留 `tabro-agent` 和原有已授权应用。`tabro-api-gateway` 是独立计费 producer，不能加入这个应用白名单。环境变量对数组按整组覆盖，配置文件增加应用后还须检查是否有更高优先级的旧数组覆盖。

API 管理后台按以下顺序操作：

1. 保存 OIDC 登录配置并开启「仅 OIDC」。已有登录客户端 `tabro-llm` 及其登录密钥保持原配置。
2. 在「Auth 计费连接配置」填写 `tabro-api-gateway` 的专用计费密钥，点击「验证并配置」。服务地址直接使用已保存的 OIDC issuer；未保存的页面输入不参与验证。已有同一 issuer、同一 producer 的密钥可留空保留。
3. 验证成功后，手动打开全局「启用 OIDC 计费」，设置计费倍数和账单时区，再保存系统设置。默认倍数为 `1`，账单按北京时间自然日汇总；真实费用逐笔扣款。

自动配置先读取已保存 issuer 的 OIDC discovery，核对返回的 issuer 完全一致，并要求 discovery 和 Token 地址使用同源 HTTPS，拒绝重定向；随后通过专用计费凭据申请包含上述六项权限的服务 Token。只有检查成功才保存连接配置，不保存或回显 Token，也不修改登录客户端或自动打开全局计费开关。该检查验证连接及服务凭据，不代替实际请求的 Auth 账户、资金和 Quote/Reserve 授权。

高级参数默认折叠。自动配置使用 `audience=tabro-llm`、`llm.invoke`、必需的 `tenant_id` 与 `act`，委托深度为 `1`，不会通过关闭租户或 Actor 校验来简化接入。模型应用和 Actor 白名单默认均为 `tabro-agent`；同一 issuer 已有的非空白名单会保留。网页登录客户端 ID 不会自动加入这些白名单；增加调用应用时须同时在 Auth 授权。

普通 API Key 仍需在 Auth 登记 credential binding 并配置中心资金路由。仅完成 OIDC 网页登录、按邮箱找到同一用户或打开计费开关，都不能替代该登记。无需逐用户登记的自动模式使用 Auth 签发、经过网关校验的模型 access token；网页登录 JWT 或 ID token 不能冒充模型调用凭据。

专用密钥轮换时先同步 Auth 的 producer 凭据与部署配置，再在同一计费身份下重新验证保存；runtime 会使用新配置，已预扣调用继续沿用冻结责任与金额。切换 producer、Auth 服务地址或 issuer，则须先停用全局计费并完成待结账操作，再关闭连接器并切换；有未结 operation 时服务拒绝关闭或改换计费身份。保存与准入的并发保护目前在单 API 实例内生效，多实例部署切换身份还须协调所有实例的收费准入，不能依赖单个实例的锁。

### 仅 OIDC 模式下的实际用量计费

后台「系统设置 → OIDC 登录」提供 OIDC 计费开关。先保存并验证仅 OIDC 登录模式，启用有效的 Auth 计费连接器和网关 Resource Server；网页登录与网关须使用同一个 issuer。`oidc_billing_supported` 表示连接配置具备接入条件，实际请求仍须通过 Auth 的 Quote、Reserve、Dispatch 授权。

| 设置 | 默认值 | 作用 |
|---|---|---|
| `oidc_billing_enabled` | `false` | 开启全站 Auth 实际用量计费 |
| `oidc_billing_rate_multiplier` | `1` | 实际积分 = 网关模型/媒体费用 × 分组或用户倍率 × OIDC 计费倍数 |
| `oidc_billing_settlement_timezone` | `Asia/Shanghai` | 账单按天汇总使用的 IANA 时区 |

新请求使用显式 `charge_mode=actual_usage`。调用前 Auth 校验真实用户、租户、应用、成员资格、可用资金及预算，建立**零金额**的执行授权，不按模型最大输入/输出量冻结积分。Quote、Reserve 和 Dispatch 必须回显支持该模式，否则网关拒绝执行，不能静默退回旧预占。每次请求仍冻结模型价格、分组/用户倍率和 OIDC 倍数。

完成后，网关把可信实际用量、应扣积分、价格证据和 Settle outbox 在同一个事务中保存。Worker 立即投递逐笔扣款，正常轮询间隔约 5 秒；按天汇总只用于账单展示，不再次扣款，也不等到零点再释放差额。模型没有返回完整用量时保留待核对，不能把失败或断流直接当作零费用。

Auth 按冻结价格与真实金额原子检查并扣款，不透支余额或其他成员预算。若并发请求完成后资金或配额不足，Auth 先持久保存该笔实际用量，再返回 `409 billing_actual_usage_pending_funds`；网关只把这个明确的 409 视为可重试。Auth 阻止该账户后续调用，充值或恢复额度后用相同事件重试；不同金额、证据或身份不能覆盖原账单。其他冲突继续拒绝。已开始执行的并发请求仍可能产生费用，因此未清偿明细不能删除或按零释放。

钱包和成员预算按实际付款时有效的周期计入；Key 的总额和窗口配额按调用时冻结的窗口计入，不能把历史费用移到新窗口绕过限额。未执行的零金额授权跨窗口后须重新准入；已执行请求仍可跨日结清。历史窗口额度不足时保留待支付记录，需要审计后的人工处理，不能通过普通新周期提额或删除明细解决。

开启后仍停用本地余额、套餐、兑换券和相应资金变更 API。普通 API Key 必须已有 Auth credential binding 与中心资金路由，不能从本地余额补扣。自动建立内部路由 Key 不等于迁移旧资金，关闭开关也不允许 Auth-only 身份回退本地扣款。

需先更新 **tabro-auth** 并应用其 actual-usage 迁移，再更新本项目。迁移 `131_complete_measured_oidc_charges.sql` 将已有、完整记录真实积分的旧待结算事件提前投递，以释放多预占部分；不修改事件、价格或幂等身份，也不自动释放用量未知或已被阻断的记录。已有 operation 保留原模式与截止时间用于幂等核对，但升级后收到完整真实积分时也立即投递；新增 operation 不设日结等待。`oidc_billing_settlement_time` 仅兼容历史配置，后台不再提供新请求的日扣款时间设置。`settlement_pending` 表示已记账但 Auth 尚未确认扣款，不能显示为已扣款。

### 模型价格、统计与计费明细的倍率

OIDC 计费开启时，模型价格页直接显示已换算的 Auth 积分价格：基础模型价格 × 用户专属倍率（未配置时使用分组倍率）× OIDC 计费倍数。有效倍率包含上述两个倍率，文本、缓存、优先级、图片、媒体单位价和阶梯价采用相同规则；前端不再重复相乘。关闭 OIDC 计费时保持原有价目。配置读取失败或倍率无效时不返回未经换算的价目。

历史用量的实际扣费、统计、图表和导出使用每次调用已保存的实际费用与最终倍率，不根据当前设置重算。Auth 计费明细中的输入、输出、缓存费用和单价按该记录的冻结倍率显示；原倍率列仅保存四位小数时，优先用记录中的实际费用与基础费用比例对齐分项精度。基础价格和上游账号成本保留独立口径。计费来源由已提交的用量账本识别，与 token、图片或按次等计价模式分开，因此旧记录也可正确识别。这里的实际费用是本次调用已记录的应扣积分，是否已实际扣款以中心 operation 状态为准。

### Auth 展示逐请求明细与日汇总

`GET /v1/billing/usage` 为独立只读接口，不创建本地 Key、不走模型路由或执行授权，也不收费。Auth 展示端使用对应应用、当前用户和工作区的模型访问 Token：`Authorization: Bearer <aud=tabro-llm, scope包含llm.invoke>`。Token 仍须通过当前 issuer、签名、有效期、客户端和委托链校验；普通网页登录 JWT、供应商 Key、计费 producer Token 不能替代。服务不得仅凭用户自报 ID 读取他人的账单，跨应用汇总应使用每个应用各自合法签发的用户 Token。

查询可用 `date=YYYY-MM-DD`，或 `start_date` / `end_date`（包含结束日，最多 90 天）；`timezone` 默认采用后台账单时区。支持 `operation_id`、`page`（默认 1）、`page_size`（默认 50，最大 100）。不接受用户、租户、应用或 Key ID 筛选；这些身份只来自已验证 Token。示例：`/v1/billing/usage?date=2026-10-10&page_size=50`。

返回 `unit=credit`、`timezone`、`page`、`page_size`、`total`，以及：

- `items`：每次调用的 `operation_id`、`request_id`、模型、输入/输出/缓存 tokens、图片数、实际应扣 `credits`、冻结 `rate_multiplier`、`state`、用量记录时间 `created_at`；仅已扣款记录含 API 确认付款时间 `settled_at`。
- `daily`：整个查询范围内的每日次数、实际消费总积分 `credits`（包含已免单消费）、`settled_credits`、`pending_credits`、`waived_credits`；总消费 = 已扣 + 待扣 + 免单，不受当前页大小截断。

所有金额和倍率以十进制字符串返回。应扣金额不等于已经扣款：只有 `state=settled` 已由 Auth 确认扣款；`settlement_pending` / `reconciliation_required` 等仍待处理，`released` 表示释放或明确免单。Auth 可按天列出合计、展开逐笔记录；这些展示汇总不能再触发扣款。

数据来自与用量和 outbox 原子提交的 `gateway_usage_ledger`，不使用尽力写入的 `usage_logs`。迁移 `132_gateway_billing_usage_identity.sql` 加入不可变计费操作关联；旧数据只在证据、主体、工作区、金额一致且唯一对应时回填，无法证明的记录不对外猜测归属。

### 页面头部的 Auth 积分与账户菜单

启用 OIDC 计费后，API 页面头部显示 Auth 当前工作空间中当前成员的可用积分，头像菜单提供充值与套餐、订单与发票、账户与账单、个人资料及安全设置入口。积分链接进入 Auth 积分明细；充值等资金入口也进入 Auth，不调用本站旧钱包、套餐或券接口。本站退出登录保持原行为。

本功能须同时更新 Auth 与 API。API 的 `/api/v1/auth/oidc-account` 仅从当前会话已验证的 OIDC Key 读取 issuer、subject 和已保存的登录客户端 ID；不接受调用方指定其他用户或工作空间。浏览器随后凭 Auth 登录 Cookie 请求 `/api/account/header-summary`。Auth 只允许当前 OIDC 登录客户端已登记的 HTTPS 回调地址来源读取结果，并再次核对当前用户状态与工作空间成员关系。仅当两个会话的 issuer、subject 完全匹配，页面才显示积分并使用 Auth 返回的工作空间生成资金菜单链接。接口不持久保存或转发上游 OIDC access/refresh token，不扩大计费服务客户端查询其他用户余额的权限。

可用积分复用 Auth 页头的计算规则，包含预扣冻结、到期、成员分配和预算限制。API 登录身份未关联、Auth 会话过期、两边账号不一致或查询失败时显示 `—`，不会以本地余额或假定的 `0` 替代。页面加载、头像菜单打开、窗口重新获得焦点和定时刷新会重新查询。浏览器限制跨站 Cookie 时也显示不可用并保留账户中心入口；`llm.tabro.cn` 与 `auth.tabro.cn` 的 HTTPS 部署属于同站，可使用现有 Auth 登录 Cookie，无需放宽整个站点的 Cookie 策略。工作空间链接使用 Auth 当前已验证的工作空间，不等于所有 API Key 的固定付款账户。

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

仅 OIDC 模式的新请求按实际用量扣款，模型能力上限仅用于校验计量和冻结价格，不作为积分占用额。未启用该模式的旧中心路径仍使用最大额预占。

1. 网关按已验证的分组、渠道模型映射、用户倍率和现有价目计算积分最大额，冻结价格快照。自动模式下，Quote 由 Auth 的 subject proof 确认真实用户/租户并选择付款账户、owner epoch、资金来源与 `gateway:credits` 技术价格版本；中心不计算模型或媒体价格。
2. 本地 operation intent 持久化后 Reserve；超时查询原 operation，内容冲突拒绝。
3. 本地 CAS 获得一次执行权，再确认中心 Dispatch，最后才调用供应商。HTTP 重定向和隐式 POST 重试不能再次发起收费工作。
4. 真实用量、供应商额度效果、证据账本和 Settle outbox 在一个本地事务中保存。中心模式不改用户旧余额、套餐 used 或客户 Key 金额计数。
5. Worker 用数据库租约投递同一不可变事件。中心提交、本地确认丢失可重放；已投递事件的 expected_version 不随查询到的新版本变化。

WebSocket 每次 `response.create` 都有独立 operation。执行许可在每次实际 frame write 前检查，包括内部恢复尝试。后续轮次被拒绝时，已经发出的轮次仍继续读取到结束或对账超时，以保存能获得的实际用量。

异步媒体在提交前保存任务身份、冻结报价和责任快照。视频任务/批量语音由数据库租约轮询；Azure 的上游任务 ID 在 PUT 前保存，响应丢失后仍可查原任务。任务完成后 `usage_recorded_at` 表示本地用量/事件已提交，中心资金状态另见 `billing_center_operations`，不能把它等同为中心已扣款。

上游调用后崩溃、缺 usage、失败但可能产生费用等状态保留为待核对，不盲目重发供应商，也不根据 HTTP 失败自动释放资金。尚未 Dispatch 的请求才可自动排队 Release。

## 产品与用量契约

中心模式向 Auth 发送唯一 `product_key=gateway:credits`、`service_tier=default` 和 `credit_amount`（通用积分）。网关保留下面各业务 meter 的请求上界与真实用量作证据，并把实际积分金额作为唯一结算 meter。所有金额和数量均为十进制字符串；实际额向上取到 10 位小数；actual_usage 的初始 `credit_amount` 为 `"0"`，显式免费价最终扣零，缺价或无可验证上界则拒绝执行。旧中心模式继续传最大积分额。管理员调整价目只影响下一请求；当前请求、流式轮次和异步媒体作业使用冻结快照。

下表中的产品和 meter 是网关内部的业务身份与用量证据；中心资金路由只需上述通用积分产品。

| 工作 | product_key | service_tier / meters |
|---|---|---|
| 文本 / Gemini | `ai:<model>` | default/priority/flex/anthropic_fast；input/output、cache read/write TTL、image output、request/image count |
| 视频 | `ai:<model>` | 大写分辨率如 `720P`；`video_seconds`、`request_count` |
| ASR | `ai:<model>` | default；`audio_seconds`、`request_count` |
| TTS | `ai:<model>` | default；`audio_characters`、`request_count` |
| 声音克隆登记 | `ai:qwen-voice-enrollment` | default；`request_count` |
| 图片生成/编辑 | `ai:<model>:images` | 小写 `<size>:<quality>`，省略项为 `auto`；`image_count`、`request_count` |

图片按实际生成张数与冻结的渠道单价或分组/目录图片单价结算；旧中心模式按请求张数预留。编辑请求的内容指纹包含原始上传字节，而账单快照不保存文件。渠道档位、分组尺寸价格和目录图片价格都未配置时拒绝执行；显式配置为 0 的单价才表示免费。

缓存计量互斥：使用 5m/1h 明细时不再重复计入 aggregate cache writes；图像输出从总 output 中剔除后单独计量。媒体实际时长保留十进制精度，不用用户请求的 duration 代替上游真实值。

目前以下情况仍需专门计量契约，中心路径会明确拒绝，shadow 不改变原请求：Responses `/compact`、后台 Responses、未单独计量的 hosted tools、无法验证上限的未知模型/自动视频规格、无法在执行前确定价格的上游模型定价，以及 Gemini 生成图片。不能把这些拒绝解释为已完整支持。文本中的图片输入可按模型完整输入上限校验计量；文件、视频及专用音频 token 输入尚需补充对应可验证上限与计价。已登记的旧 API Key 还必须在 Auth 审核其绑定产品包含 `gateway:credits`，否则中心明确拒绝，不能绕过绑定限制。

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
