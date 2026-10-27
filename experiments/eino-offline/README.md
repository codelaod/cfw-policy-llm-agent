# Go + Eino 最小技术验证

这是独立实验 module，不是正式审计后端。默认 `run`/`test` 使用人工样例和脚本模型；显式 `live` 才连接配置的模型服务，输入仍是人工样例，不使用云账号。

2026-10-22 已完成两个 MiniMax 人工样例的真实调用，见 [初测记录](../../docs/validation/experiments/live-model.md)。本次都是一批工具结果回传，不能据此说连续多轮补查、流式、完整协议或生产审计已通过。

## 依赖与环境

- 已验证 Linux/WSL、Go 1.26.10；`go.mod` 声明 Go 1.24.0，其他版本未逐一验证。
- Eino v0.9.22；OpenAI 兼容模型组件 v0.1.13。直接和间接依赖均由 `go.mod`、`go.sum` 固定，不是自己重写通用 Agent 或模型 SDK。
- 首次 `prepare` 联网下载依赖，缓存放在 `.cache/`；不修改全局 Go 配置，不提交缓存。
- 其余动作关闭 Go 模块代理/校验联网并禁用自动下载工具链。`live` 仍会按显式授权访问模型，不要把关闭模块联网说成关闭所有网络。
- 默认离线模式不需要 Node、数据库、Redis、Docker、模型密钥或阿里云凭据。资料检查用 Python 3.9+ 标准库。
- `-race` 另需平台支持和 C 工具链，不是普通运行前提。

## 默认离线验证

从仓库根目录运行：

```bash
bash scripts/eino-offline.sh prepare
bash scripts/eino-offline.sh test
bash scripts/eino-offline.sh vet
bash scripts/eino-offline.sh run
# 可选
bash scripts/eino-offline.sh test -race
```

可选择脚本样例：

```bash
bash scripts/eino-offline.sh run -scenario whitelist
bash scripts/eino-offline.sh run -scenario missing-whitelist
```

[脚本示例报告](example-report.json) 明确标记 `scripted_model`，路径和回答是预设，不证明真实模型自主决策。真实 HTTP 适配测试默认只使用本机模拟服务，不加载根目录私有文件、不访问外部模型。

## 私有模型配置与无请求检查

根目录 `.env.local` 被 Git 忽略。新机器可以从 [空白模板](../../.env.example) 创建私有文件，不能在模板中填真实密钥：

```bash
cp -n .env.example .env.local
chmod 600 .env.local
```

必填 `CFW_MODEL_BASE_URL`、`CFW_MODEL_API_KEY`、`CFW_MODEL_NAME`。

- URL 填服务商的 Base URL，保留必要路径，不填写完整 `/chat/completions` 或 `/responses`；程序不擅自补 `/v1`。
- HTTPS 远端或本机 HTTP；不允许 URL 里携带账号密码、查询参数或片段。
- 密钥只本地填写，不发到聊天、Git、命令行参数或报告。
- 进程同名环境变量优先于文件；显式空值也会覆盖文件。
- 支持普通赋值、单/双引号、`export`、BOM 与 Windows 换行；不支持行尾注释，不执行 shell 或变量展开，不要 `source` 私有文件。

```bash
bash scripts/eino-offline.sh config-check
```

只输出字段是否已填、协议和限制；不输出实际地址、模型名和密钥，`request_sent=false`。错误只显示通用类别/变量名/行号，不回显原始值。格式通过不等于服务连通或协议兼容，填配置不是付费调用授权。

## 显式真实模型验证

当前只实现**非流式 Chat Completions**。`responses` 虽可登记到配置，执行时明确拒绝，不静默降级；这不是声称厂商不支持 Responses。

在用户已授权的范围内，单次只选择一个人工场景：

```bash
bash scripts/eino-offline.sh live -provider minimax -allow-paid-requests -scenario whitelist
bash scripts/eino-offline.sh live -provider minimax -allow-paid-requests -scenario missing-whitelist
```

普通兼容服务可使用 `-provider openai-compatible`，但必须单独验证，不能因 MiniMax 个例通过就宣称兼容。MiniMax profile 设置 `reasoning_split=true`；必要的完整消息只保留在内存中用于协议连续性，内部思考不进入报告。

没有 `-allow-paid-requests` 会在读配置和创建请求前拒绝。默认 `run`/`test` 不因填写密钥就切换模式。

### 限制与保护

- 单次命令 HTTP 请求上限来自 `CFW_MODEL_MAX_CALLS`（默认 4，范围 1～8）；迭代/模型回调也受限。
- 单次请求超时来自 `CFW_MODEL_TIMEOUT_SECONDS`（默认 30，范围 1～120）；整体运行另有按请求数计算的期限。
- 输出参数来自 `CFW_MODEL_MAX_OUTPUT_TOKENS`（默认 512，范围 16～4096），映射到 `max_completion_tokens`；不得与思考格式开关混淆。
- HTTP 层核对目标地址、模型标识、认证头、输出限额和非流式模式；拒绝重定向并限制请求/响应大小。
- 限制是真实 HTTP 尝试计数，不仅是框架回合数；错误转换为脱敏类别，不输出原始服务错误正文。
- 截断不会自动提高输出上限；服务报告超出请求限额时停止。usage 缺失时保持未知，不伪造零。
- 每次命令独立计数，多次执行会重置单次上限，**不是跨命令或账户金额上限**。本轮两个场景按剩余预算分配，合计只用了 4 次；不要照抄两条命令就误认为总共最多 4 次。

### 工具与结果边界

只有 `get_asset`、`get_business_intent`，只访问当前任务的人工资产。工具事件记录实际调用 ID、结果证据和错误；参数、对象、证据与最终来源由本地程序校验，模型不能修改已给定白名单或用不存在的证据。

真实模式支持同批多个工具分别回传。`tool_result_roundtrips` 统计被核对的**工具结果条数**，不是连续补查回合数。MiniMax 本次真实调用只观察到一批工具；连续工具回合在本机模拟测试通过，仍需真实场景补证据。

报告只保留允许的结构、事件、终止原因与服务用量，不记录完整内部思考。原始运行记录放忽略目录；公开的 [脱敏人工实测数据](../../docs/validation/evidence/live-model-2026-10-22.json) 已归一化工具 ID，不含精确端点、实际模型标识和凭据。

## 当前没有验证的内容

- 真实模型连续两轮以上工具补查、流式、Responses 或原生 JSON Schema 输出。
- 模型稳定性、完整审计准确率、提示注入防护效果或自然语言解释的全面正确性。
- 阿里云 API 权限、版本、分页、策略语义和实际可达性。
- 正式 Web/TUI、完整规则引擎、持久化、断点恢复或生产并发。

## 源码职责

- `fixture_model.go`：确定性脚本驱动，不是大模型。
- `live_model.go`：模型组件适配、HTTP 保护、真实工具结果回传与脱敏；不实现流式。
- `probe.go`：领域工具、任务范围、证据与结果校验、共用 Eino 执行。
- `model_config.go`：私有配置读取和脱敏无请求检查。
- `*_test.go`：脚本、本机 HTTP 服务、配置与安全边界测试，默认不使用真实服务。
- `main.go`：默认脚本/配置检查/显式真实模式三个互斥入口。

这仍只是选型证据的一部分，不自动锁定正式语言或框架。
