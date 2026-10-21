# Go + Eino 离线集成验证

这是独立的技术验证 module，不是正式审计后端。模型是本地确定性脚本，不是大模型；数据全部人工构造，不使用云账号、真实资产或模型密钥。

## 依赖与环境

- 已验证环境：Linux/WSL、Go 1.26.10；本实验 `go.mod` 声明 Go 1.24.0，未逐一验证其他 Go 版本。
- 直接依赖：`github.com/cloudwego/eino v0.9.22`；间接依赖由 `go.mod`、`go.sum` 固定。
- 初次准备需要联网下载 Go modules；缓存放在仓库 `.cache/`，不改全局 `go env`、不提交依赖缓存。
- 准备后执行脚本会设置 `GOPROXY=off`、`GONOPROXY=none`、`GOSUMDB=off`、`GOTOOLCHAIN=local`，不自动下载模块或工具链。
- 不需要 Node、Python 包、数据库、Redis、Docker、阿里云 SDK 或模型服务。资料校验脚本另需 Python 3.9+，只使用标准库。
- `-race` 检查额外要求当前 Go 平台支持 race detector，且具备 C 工具链；正常测试不以它为运行前提。

## 从仓库根目录运行

```bash
# 新机器首次执行：需要联网，下载已锁定依赖。
bash scripts/eino-offline.sh prepare

# 后续不使用模块联网，也不访问模型或云平台。
bash scripts/eino-offline.sh test
bash scripts/eino-offline.sh vet
bash scripts/eino-offline.sh run

# 可选：当前平台支持时运行。
bash scripts/eino-offline.sh test -race
```

也可以单独选择样例：

```bash
bash scripts/eino-offline.sh run -scenario whitelist
bash scripts/eino-offline.sh run -scenario missing-whitelist
```

报告写到标准输出；[示例报告](example-report.json) 是本次运行产生的纯人工样例输出，允许随代码保存。其他运行记录可重定向到已忽略的 `runtime/`，不要把真实执行日志和凭据提交到 Git。

## 私有模型配置：先校验，不发送请求

仓库根目录的 `.env.local` 用于本地填写模型参数，已由 `.gitignore` 排除。可提交的空白模板是 [`.env.example`](../../.env.example)；模板里不能填写真实密钥。本轮已创建空的私有文件，不覆盖已有私有配置。

必填项：

```dotenv
CFW_MODEL_BASE_URL=https://your-model-provider.example/v1
CFW_MODEL_API_KEY=
CFW_MODEL_NAME=
```

- 地址填写服务商提供的 **Base URL**，保留其必要路径，不填写完整 `/chat/completions` 或 `/responses` 请求地址；程序不擅自补 `/v1`。
- 使用 HTTPS；HTTP 目前仅允许 localhost/环回地址。若实际服务需要其他形式，应先明确评估，不把密钥放进 URL 查询参数或账号密码部分。
- 密钥只在本地填写，不发到聊天、Git、命令行参数或报告中。
- `CFW_MODEL_API_STYLE` 可登记 `chat_completions` 或 `responses`，默认前者；**只登记协议，不代表两种真实适配器已实现**。
- 超时默认 30 秒（1～120），模型调用数默认 4（1～8），每次最大输出 token 默认 512（16～4096）。这是后续接入使用的保护配置，不是金额上限；目前尚未用于实际模型请求。

新机器在私有文件不存在时可复制模板并设置权限：

```bash
cp -n .env.example .env.local
chmod 600 .env.local
```

填写后，从仓库根目录执行：

```bash
bash scripts/eino-offline.sh config-check
```

该命令自动定位仓库根目录的私有文件，只检查配置并输出脱敏 JSON：仅显示字段是否已填、协议标记和限制数值，不显示密钥、实际服务地址或模型标识。`request_sent` 固定为 `false`；失败时只显示变量名/行号与通用原因，不显示原始值。

读取优先级是同名进程环境变量高于文件；显式空环境变量也会覆盖文件并导致必填校验失败。文件支持 `KEY=value`、单/双引号、`export` 前缀、UTF-8 BOM 和 Windows 换行；不执行 shell、不展开 `$VAR`/命令替换，不支持行尾注释。不要用 `source .env.local` 代替本程序解析。

离线 `run`、`test` 不读取 `.env.local`，填写密钥也不会触发真实请求。当前只有配置读取/校验入口，真实模型适配器与付费调用需下一步实现和确认。不能把 `config_valid=true` 说成模型协议兼容或服务连通。

## 验证内容

实际使用 Eino `ChatModelAgent`、`Runner` 和工具节点，而不是自己模拟整个执行器：

- `whitelist`：脚本先查询资产，再查询业务意图，工具结果回传后输出有条件建议。
- `missing-whitelist`：脚本只查询业务意图，确认样例缺少白名单后结束，保持待确认。
- 检查工具调用 ID 在结果回传中的一致性、事件顺序、成功工具返回的证据与最终引用。
- 工具 allowlist、单任务对象范围、严格 JSON 参数、结果字节数和调用预算由实验的领域层约束。
- 迭代上限由 Eino 执行，同时测试模型/工具调用上限、取消、超时、工具错误及不合法最终输出。
- 通过白名单、缺少白名单、非法工具/参数/对象、编造证据/来源等正反案例验证失败不会被报告为成功。

输出 `mode` 固定为 `scripted_model`。工具路径和最终建议由测试脚本预设，不能据此证明模型会自主决策或推荐质量有提升。

## 没有验证什么

- 任何真实模型服务的 Chat Completions/Responses、流式、token 统计、结构化输出兼容性。
- 大模型真实工具选择、中文理解、提示注入防护效果或建议质量。
- 阿里云 API 权限、分页、产品版本、策略匹配和跨安全组可达性。
- 正式 Web/TUI、完整规则引擎、持久化、断点恢复或生产并发。

后续真实模型验证需要服务地址、模型标识及服务端/本地私有凭据，不能把密钥写入样例、Git 或报告。只有显式 `config-check` 读取这些模型配置；离线样例不读取，也不发起模型请求。

## 源码职责

- `fixture_model.go`：实现 Eino 模型接口的确定性测试驱动，明确不实现流式。
- `probe.go`：领域工具、任务边界、证据与最终输出校验、Eino 集成。
- `probe_test.go`：正反路径、预算、错误、取消与超时测试。
- `main.go`：运行人工构造样例，或进入独立的无请求配置检查。
- `model_config.go` / `model_config_test.go`：私有配置加载、脱敏、边界校验与不发请求测试。

本实验只是选型证据的一部分，不自动锁定正式系统语言或框架。
