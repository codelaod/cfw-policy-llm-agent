# E0 固定协议探针

`protocol_test.go` 只使用 `httptest` 的 loopback 地址和明确构造的假凭据；不读取项目私有配置、进程凭据或机器 profile。HTTP 客户端禁用代理，按固定 host 允许请求并拒绝重定向；初始化不发请求。

探针覆盖：

- Eino v0.9.22 / openai 组件 v0.1.13 实际调用非流式 Chat Completions 的请求与响应。
- 固定 Cloud Firewall SDK `c44859c166b7` 的 `DescribeNatFirewallListWithContext` 到物理 HTTP 的取消传递。
- 显式假凭据 Provider 的本机交换和 SDK 内部重试共用预记次数上限；取消／送达未知不返还。
- 在 SDK JSON 解码前限制响应正文，替换上游错误正文，拒绝重定向及非 loopback 目标。

在 `server/` 运行：

```bash
CGO_ENABLED=0 go test ./internal/protocolprobe -count=1 -v
```

这些助手只在测试文件中存在。这里的内存账本不构成正式持久预算；本机模拟交换不代表官方 RAM/STS 续期、权限、账号能力、真实云接入或真实模型能力已验收。正式 Provider 仍须显式绑定任务的 context、准入和传输；不能直接把没有 context 参数的 `GetCredential()` 视为已满足取消。

固定 Cloud Firewall 依赖取已核读提交对应版本 `v11.1.1-0.20261009173233-c44859c166b7`；其依赖路线是 darabonba-openapi/v2 v2.2.4、tea v1.5.2、credentials-go v1.4.5。`Config.HttpClient` 的每实例注入进入 `dara.DoRequestWithCtx`；不修改 SDK 全局客户端。
