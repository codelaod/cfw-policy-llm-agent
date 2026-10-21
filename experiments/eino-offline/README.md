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

后续真实模型验证需要服务地址、模型标识及服务端/本地私有凭据，不能把密钥写入样例、Git 或报告。本实验不读取这些变量，避免误用付费服务。

## 源码职责

- `fixture_model.go`：实现 Eino 模型接口的确定性测试驱动，明确不实现流式。
- `probe.go`：领域工具、任务边界、证据与最终输出校验、Eino 集成。
- `probe_test.go`：正反路径、预算、错误、取消与超时测试。
- `main.go`：运行人工构造样例并输出 JSON。

本实验只是选型证据的一部分，不自动锁定正式系统语言或框架。
