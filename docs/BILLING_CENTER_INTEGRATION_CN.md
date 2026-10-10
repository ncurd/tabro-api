# 接入 Auth 统一计费中心：网关改造清单

状态：原设计与验收基线，代码已按本方案实现；实际路径与明确限制见 [网关部署说明](BILLING_CENTER_GATEWAY_CN.md)，部署/迁移命令见同级 Auth 仓库 `docs/BILLING_DEPLOYMENT_AND_MIGRATION_CN.md`。生产尚未切流。日期：2026-09-23。

主方案位于同级仓库 `tabro-auth/docs/UNIFIED_BILLING_PLAN_CN.md`，规定身份、团队模式、账本、协议和切流规则。本文给出 `tabro-api` 的实施位置与验收要求；当前运行行为仍以代码及 [OIDC 配置文档](OIDC_RESOURCE_SERVER_CN.md) 为准。

## 1. 目标边界

`tabro-api` 只作为内部模型网关和运维后台。Auth 账户中心承担客户余额、套餐、成员预算、零售价格、充值退款和客户账本；应用不再各自维护可修改的客户余额。

网关继续负责上游凭据、渠道、模型映射、重试、模型目录、原始用量、供应商账号额度及成本。现有 `AccountID` 是供应商账号，`Group` 是路由/模型策略及旧套餐分组，都不能改作团队身份。

每个付款账户有且只有一个客户资金写入方。迁移中 `local/shadow/central` 模式按账户固定，不能按请求随机路由；shadow 不修改真实中心余额，central 不能降级为本地客户扣费。

## 2. 必须保留的旧语义

| 当前实现 | 必须保留/明确的事实 |
|---|---|
| [gateway_resource_server.go](../backend/internal/service/gateway_resource_server.go)、[api_key_repo.go](../backend/internal/repository/api_key_repo.go) | `(iss, sub)` 映射到用户自己的 Key；租户与付款方需要独立解析 |
| [auth_oidc_oauth.go](../backend/internal/handler/auth_oidc_oauth.go) | OIDC 登录会使用绑定 Key 的 UserID 建立本地登录；不能把所有成员映射成团队所有者 |
| [gateway_request_identity.go](../backend/internal/server/middleware/gateway_request_identity.go) | 已记录部分 OIDC 身份；需要把经验证的来源 app、actor 与付款上下文贯穿请求和任务 |
| [usage_billing_repo.go](../backend/internal/repository/usage_billing_repo.go) | 当前同库去重、客户与供应商效果、消费账本一起提交；拆远程结算后不能宣称跨库仍原子 |
| [114_add_gateway_usage_ledger.sql](../backend/migrations/114_add_gateway_usage_ledger.sql) | 已有消费事实账本可用作迁移来源；`usage_logs` 是尽力记录，不是完整资金账本 |
| [billing_cache_service.go](../backend/internal/service/billing_cache_service.go) | 现有缓存资格检查不预占；中心模式不能继续依赖旧 user/group 缓存决定余额 |
| [billing_service.go](../backend/internal/service/billing_service.go)、[gateway_service.go](../backend/internal/service/gateway_service.go) | 旧套餐扣 `TotalCost`，旧余额/Key 额度扣 `ActualCost`；`TotalCost` 不是供应商成本 |
| [account_stats_pricing.go](../backend/internal/service/account_stats_pricing.go) | 供应商统计定价可独立覆盖，留在 Go |
| [payment_currency.go](../backend/internal/service/payment_currency.go)、[payment_order.go](../backend/internal/service/payment_order.go) | 实付币种金额与入账 credits 不同；余额不能直接改称 USD |
| [subscription_service.go](../backend/internal/service/subscription_service.go) | 旧日/周/月为本地日期零点起算的 24h/7d/30d，迁移保留起点、used、有效期及时区 |
| [usage_record_worker_pool.go](../backend/internal/service/usage_record_worker_pool.go) | 主路径已有关键任务同步尝试，但没有完整持久 intent/outbox，崩溃后仍可能丢失待结算工作 |
| [media_billing_service.go](../backend/internal/service/media_billing_service.go) | 媒体已有价格/身份快照与结束后结算，需扩展资金责任快照 |

## 3. 接口与身份适配

新增不可变 `BillingContext`，贯穿文本请求、重试、流式完成、媒体任务与结算事件：

```text
actor_user_id / issuer / subject
tenant_id / origin_app_id / producer_client_id
billing_account_id / member_budget_id
billing_mode / billing_owner_epoch
operation_id / reservation_id
price_version / product_version / wallet_unit / period_ids
```

Auth 负责返回付款方和预算，不信任用户 Header 指定 payer。Go 本地用户 ID 通过显式映射关联 Auth 用户 ID，不能靠邮箱自动合并。Key 继续表达本地路由/权限约束，不再隐含“这个 UserID 必然就是最终付款人”。

新增独立服务客户端 `tabro-gateway-billing`，Bearer audience 为 `tabro-billing`；现有模型 token audience 保持唯一 `tabro-llm`。Reserve 时另附模型 token 作为 subject proof，中心独立验证并实时查成员资格；不在日志/outbox 中持久保存原 token。Settle 仅需服务凭据及绑定本 producer 的持久 reservation，不依赖长任务用户 token 仍有效。

旧 API Key 请求先在中心登记 `credential_binding_id + version`，绑定 actor、允许的 tenant/app、Key 限额与状态；网关完成 Key 校验后只提交登记引用。中心不接受普通请求临时传任意 user/tenant 代扣。无法验证来源 app 的旧 Key 明确记作 `legacy-api-key`。用户 Key 额度继续是额外限制，不能代替跨 Key 的团队成员预算；中心模式下客户金额额度也须在中心原子预占和消耗，本地仍可执行 QPS 等网关保护。

幂等键由可信 producer/app、稳定 operation ID 和操作类型组成，内容指纹包含 actor、tenant、请求、价格与计量。重复事件相同则返回旧结果，冲突则拒绝。不能继续把可轮换 `api_key_id` 作为唯一业务幂等边界。业务含多次模型调用时每次有独立子操作 ID，上游重试有独立 attempt ID，但不自动增加客户收费次数。

## 4. 持久化与调用顺序

建议新增：`billing_center_client.go`、`billing_context.go`、`billing_operation_service.go`、`billing_outbox_worker.go` 及相应 repository/migration。名称是实施建议，不代表当前文件已经存在。

1. 网关数据库先保存 operation intent 与固定的 billing owner/epoch。
2. 调 Auth Reserve 并持久保存结果。超时查询同一操作；结果未知不得调用供应商。
3. 通过本地 operation/attempt 条件状态转换取得唯一执行权，再调 Auth Dispatch。中心检查当前资格，状态确认后才派发供应商请求；幂等成功响应不能当作再次执行许可。过期取消、成员撤权与 Dispatch 有确定顺序；已开始发送但结果未知的任务接管后只查询/核对，不盲目重发。
4. 收到实际用量，在一个 Go 本地事务中保存原始计量、本地供应商额度/成本效果以及 Settle outbox；尽快提交到中心，失败可靠重试。
5. Auth 在自身事务内完成幂等、价格计算、钱包/套餐/成员预算结算与流水。网关拿到确定结果后记录 settled，中心已提交但响应丢失则重试原事件。

`UsageBillingRepository.Apply` 应拆出明确的 `ApplyProviderUsageAndEnqueueSettlement` 路径。中心模式禁用本地客户余额、用户套餐及客户 Key 金额额度的旧写入，也禁用非原子余额降级路径；本地供应商额度和统计仍正常更新。API 中的中心账单投影只能供查询，不形成第二份可消费余额。

Outbox 包含 schema version、稳定事件 ID、计量证据引用、冻结价格/单位、reservation、producer/app、owner epoch、重试与租约状态。与用量记录同事务保存，通过唯一约束保证重放不会重复计供应商成本。不能在 Go 数据库事务里调用 Auth 网络接口来模拟跨库原子性。

供应商执行与计量入库之间仍可能崩溃。intent 和 provider request ID 用于恢复查询或标记未知，不能宣称能恢复供应商未提供的精确用量。上游不支持幂等时，不盲目重发已经派发但结果未知的调用。

## 5. 文本、流式与媒体

- 改造 [gateway_service.go](../backend/internal/service/gateway_service.go)、[openai_gateway_service.go](../backend/internal/service/openai_gateway_service.go) 的统一用量结算入口，同时覆盖不同 handler/协议，避免只改某一个模型端点。
- 当前计价异常存在 `ActualCost: 0` 回退。中心路径改为 `pending_pricing`；保留 usage、价格版本和预占等待补偿。
- 流被客户端中断或 HTTP 返回错误不代表无成本。有可信用量按产品政策结算；明确未执行才 Release；不完整 usage 进入 `pending_usage/reconciliation_required`。
- 旧预占模式须覆盖允许执行的费用上界，或在进一步产生费用前 Extend。仅 OIDC 新 `actual_usage` 模式使用零金额授权，收到完整用量后逐笔扣款；资金不足持久保存未清偿明细、阻止新调用并重试，不能静默扣成负数或挤占其他成员预算。参见 [实际用量计费](BILLING_CENTER_GATEWAY_CN.md#仅-oidc-模式下的实际用量计费)。
- [media_billing_service.go](../backend/internal/service/media_billing_service.go) 的快照增加 reservation、billing account、owner epoch、价格/权益版本与 period IDs，继续保留原模型和供应商计价证据。
- [media_billing_worker.go](../backend/internal/service/media_billing_worker.go) 和 [media_billing_reconciliation_repo.go](../backend/internal/repository/media_billing_reconciliation_repo.go) 复用现有数据库租约，分别记录 `usage_persisted_at`、`settlement_enqueued_at`、`settled_at`。原 `UsageRecordedAt` 不可独自表示中心已扣费完成。
- 已 dispatch/运行中/提交未知的媒体任务不得仅因预占 TTL 到期释放资金；续租和核对都用固定责任快照。用户退出团队后禁止新授权，原合法消费仍可结清。

## 6. 客户价格迁移

现有 [pricing_service.go](../backend/internal/service/pricing_service.go)、[billing_service.go](../backend/internal/service/billing_service.go) 及 [模型价格资源](../backend/resources/model-pricing/) 是迁移输入。

过渡期保留 Go 唯一生产零售计价器，使用中心登记的不可变价格版本与报价快照；Auth 新计价器仅影子计算。逐项验证通过后，Auth 接管客户价格发布与实际结算，Go 保留供应商计价和非权威目录/展示缓存。不能由两个定时同步器各自更新生产客户价格。

覆盖样本至少包括当前 GPT-6 Sol/Luna、Claude Opus 5.5，以及已有模型的输入/输出、缓存读写 TTL、长上下文、服务层级、优先级/Fast/Flex、用户/渠道倍率、图片数量和视频时长。样本保存完整输入、版本、TotalCost、ActualCost、套餐效果、供应商额度与成本，而不是只比最终一个金额。

新协议金额为十进制字符串；旧数据库精确余额与套餐用量直接导出。桥接期间若仍存在浮点计算，必须固定兼容舍入规则并记录差异，不以迁移为由重算旧历史。`legacy_credit`、客户价格单位、支付币种和套餐计量单位分别保存。余额充值订单的 Amount 才是换算后的入账额度；套餐订单 Amount 是商品价格，履约发放套餐权益，不能再增加钱包余额。

## 7. 所有资金入口一起切换

| 入口 | 改造 |
|---|---|
| [payment_fulfillment.go](../backend/internal/service/payment_fulfillment.go)、[payment_webhook_handler.go](../backend/internal/handler/payment_webhook_handler.go) | 验签可暂留网关；按稳定订单/支付事件键向中心入账，禁止同时走旧兑换加余额 |
| [payment_refund.go](../backend/internal/service/payment_refund.go) | 支付退款与客户额度冲回分开建模并可靠关联；重复/部分退款不重复冲账 |
| [redeem_service.go](../backend/internal/service/redeem_service.go)、用户余额管理入口 | 中心账户通过受限调整接口处理赠额/兑换/管理调整，保留原因与操作者 |
| 套餐购买、续期、重置与管理员更改 | 中心维护权益及版本，旧周期/used 按原样导入 |
| 本地数据库余额/套餐更新 | 数据库 owner fencing，旧实例或旧回调不能绕过切流继续写资金 |

生产切流：账户进入 `draining` 阻止新操作、只完成已登记旧责任 → 排空旧请求/媒体/未知记录 → 源数据库事务进入 `frozen`，阻止所有客户资金/权益写入并记录水位 → 从冻结快照导入精确余额、单位、权益周期及支付责任 → 核对摘要 → 提升 epoch，进入 `central`。导入核对完成前目标不可消费。冻结覆盖套餐激活、窗口重置、到期维护及旧支付回调，不能只关请求入口。

导入使用批次幂等，历史消费作为来源记录而非新结算事件；已完成的充值、套餐履约、退款状态和去重映射也一并导入，标记为已消费凭证。旧回调适配器对已履约事件返回原结果，仅把明确归 Auth 的未完成责任转中心，防止历史充值再次计入期初余额。初始批次只选没有未完旧消费的账户；团队建账不自动转入成员个人余额。负余额保留，不以零代替。已扣旧库的操作重试必须命中原结果，不能在 Auth 再扣一次。

Auth 故障时拒绝新的收费工作，已有工作落持久 outbox 后重试；不得回退旧钱包。应用代码回滚不回滚资金权威，反向迁移须重新冻结和对账。

## 8. 联合验收与完成标准

- [ ] OIDC 的 issuer/sub/tenant/app 与实际 actor/payer 一致；个人、两支团队切换不混账；团队付款不改变本地登录身份。
- [ ] 新 Billing audience/scope 不改变现有模型 token 验证条件；跨 producer reservation 和伪造 payer 被拒绝；旧 Key 停用/限额同步可验证。
- [ ] 多应用、多 Key 并发受同一个账户及成员预算约束；旧 Key 金额额度不会在中心、本地两次扣减。
- [ ] 旧套餐 TotalCost、钱包 ActualCost、credits 充值和 24h/7d/30d 窗口迁移一致；供应商统计与中心客户结算可独立对账。
- [ ] 预占/派发/到期取消与结算/释放竞争结果一致；价格、单位、周期版本在长任务中不漂移。
- [ ] 两个实例重放 Dispatch 不重复派发；预占后成员被移除，未派发任务不能启动。
- [ ] 在“中心提交响应丢失”“本地 outbox 已写进程退出”“中心成功、本地确认未写”等位置注入故障，重试后只产生一次资金效果。
- [ ] 计价失败、流截断、缺媒体时长均保留待核对状态，不能静默零元结清或释放正在执行的资金。
- [ ] 消费、充值、退款、赠额、兑换码、套餐及管理员调整全部遵守同一 owner epoch，迁移前后资金与权益对平。
- [ ] 迁移后再次收到迁移前成功的充值、部分退款、套餐续费回调，余额及权益不变。
- [ ] 用户通过 Auth 查账户和账单；网关前端保留内部运营功能，不再是用户入口。

实施时运行与改动相关的 Go 单元/数据库集成测试、Auth 的 OAuth/团队授权/计费事务测试及跨服务故障测试。规划文档本身只需检查引用、差异和可执行性，不据此声称接口或迁移已完成。
