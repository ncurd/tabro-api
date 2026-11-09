# Claude Code 转发兼容性

核对日期：2026-11-09。本次故障的上游错误为 `claude_code_version_too_old`：请求声明 `2.1.63`，所用模型要求 `2.1.280` 或更新版本。

## 已核对的源码

- [CLIProxyAPI，681ce41b6e44c45b15b79378b003909d6ba29e68](https://github.com/router-for-me/CLIProxyAPI/tree/681ce41b6e44c45b15b79378b003909d6ba29e68)：`internal/runtime/executor/claude_executor_request.go`、`claude_signing.go`、`claude_executor_cloaking.go`、`helps/claude_device_profile.go`。
- [sub2api，ee58c0013c76ccd3f36439881bc848d342ec1c9d](https://github.com/Wei-Shaw/sub2api/tree/ee58c0013c76ccd3f36439881bc848d342ec1c9d)：OAuth 端点、原生客户端辅助请求、Messages/count_tokens 字段净化及版本一致性处理。

两个项目并非完全相同的实现：本项目选用 CLIProxyAPI 的 2.1.280 请求身份与 CCH 签名方案，结合 sub2api 的 OAuth 与请求兼容处理，没有把两个项目各自的默认配置机械拼接。

## 当前行为

- 代理转换请求统一使用 CLI `2.1.280`、Anthropic JS SDK `0.112.1`、Node `v26.3.0`。旧缓存中的软件版本不会覆盖这组值；账号设备 ID、OS/Arch 仍保留。
- User-Agent、`cc_version`、版本 fingerprint 和 `metadata.user_id` 格式使用同一软件身份。原生 Claude Code 保留自身版本、工具名及明确的客户端特性，不会被其他客户端的缓存升级或降级。
- OAuth 的授权入口为 `https://claude.com/cai/oauth/authorize`，兑换和刷新使用 `https://platform.claude.com/v1/oauth/token`；保留 PKCE、state、刷新轮换及限流退避。
- CCH 使用锁定源码的 seed、原始 JSON 字节归一化及 JavaScript UTF-16 fingerprint 规则。签名在最终 beta 策略和字段净化之后执行，避免请求体随后变化使签名失效。
- Messages 与 count_tokens 使用各自的 beta 集合；只有实际启用且允许的字段才能随相应 beta 发出。count_tokens 不携带生成专用字段和 metadata，也不新增生成请求的签名块。
- 转换普通客户端的系统提示词时，保留完整原文；不再将其替换成一段通用提示词或注入旧版完整 Claude Code 静态说明。
- 账户连接测试复用实际转发构造与 TLS 选择。空响应和截断 SSE 不再被判为测试成功。
- 新生成的客户端配置不再设置 `CLAUDE_CODE_ATTRIBUTION_HEADER=0`。已有配置若主动关闭该字段，应移除这项覆盖。

## 验证与上线

已加入上游固定签名向量、旧缓存软件版本、原生客户端版本保留、辅助请求识别、账户测试、OAuth 兑换／刷新等回归。源码和本地协议测试不能替代真实账号联调；升级后须重新构建并重启后端。

若运行的是原生 Claude Code 且它自身版本低于模型要求，应执行 `claude update` 或更新桌面客户端。代理的新默认版本不会改变本机安装的 CLI。

CCH 字节扫描实现参考 CLIProxyAPI；其 MIT 许可与版权声明保存在 [CLIProxyAPI-MIT.txt](licenses/CLIProxyAPI-MIT.txt)。
