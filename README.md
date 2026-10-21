# 基于大模型智能体的云防火墙策略审计与推荐系统设计与实现

本仓库用于论文研究与系统实现。系统以云防火墙策略审计为业务主线，以大模型 Agent 为实现方法，围绕云资产、访问控制策略和业务访问需求，完成风险识别、证据补查、配置推荐与审计报告生成。

当前处于方案讨论与最小技术验证阶段，尚未实现正式审计业务系统。已有独立 Go + Eino 模拟模型实验；这不代表完成真实模型或云接口验证，正式技术栈仍待确认。

## 已确认的方向

- 论文面向本科毕业或一般硕士工程实践的工作量，重视完整实现与可验证结果，不追求复杂算法创新。
- 首个适配云平台为阿里云，演示使用云防火墙和 ECS；真实环境计划集中开通、集中验证。
- 首版只读取配置、开展审计和提供推荐；由人确认并在云平台手动应用策略。
- 优先实现前后端分离的 Web 系统；CLI 作为可选后续入口。
- 模型接入优先兼容 OpenAI 接口规范；具体接口路径、模型、Web 框架和 Agent 框架尚未确定。
- 云防火墙业务与 Agent 工程并行设计。用审计结果体现系统价值，用工具选择、证据补查和执行记录体现 Agent 的实际作用。

## 建议的首版范围，待后续讨论确认

以阿里云互联网边界的入向访问控制为首个闭环：读取公网资产和防护状态 → 关联访问控制策略与业务意图 → 执行规则检查和 Agent 补查 → 生成有依据的推荐与报告 → 人工修改后重新审计。

同时支持离线快照和真实只读采集。离线模式用于开发和重复实验，真实模式用于验证云平台接入；实际可采集字段、产品版本和账号能力需先做验证。

## 文档导航

| 文档 | 用途 |
| --- | --- |
| [项目定位与论文主线](docs/01-project-overview.md) | 研究问题、双主线关系、论文结构与贡献边界 |
| [需求与首版范围](docs/02-requirements-and-scope.md) | 用户流程、审计内容、业务意图、验收标准 |
| [架构与技术方案讨论稿](docs/03-architecture-discussion.md) | 前后端职责、Agent 工具、模型适配、候选选型 |
| [实验与演示方案](docs/04-evaluation-and-demo.md) | 对照实验、标注、真实云环境验证与费用管理 |
| [阶段计划与决策记录](docs/05-roadmap-and-decisions.md) | 已确认事项、未定问题、实施顺序与下一次讨论重点 |
| [官方资料与核实记录](docs/06-references.md) | 接口及产品资料来源、核实边界 |
| [技术背景与选型评估](docs/07-technical-background-and-options.md) | Go/Python/TS、Agent 框架、未来 TUI 与代表性论文/项目 |
| [云平台接入与能力映射](docs/08-cloud-integration-and-capability-map.md) | 分层只读 API、实际字段、支持边界与离线资料缓存 |
| [离线验证记录](docs/09-offline-validation-record.md) | 固定依赖、实际通过的检查及未验证范围 |
| [换环境开发交接](docs/10-environment-handoff.md) | 当前进度、迁移边界、新环境恢复步骤与下一任务 |
| [离线参考资料](docs/references/README.md) | 随 Git 保存的 11 篇官方正文、来源与校验清单 |

## 开始实现前

仓库协作约定见 [AGENTS.md](AGENTS.md)。

先确认首版审计边界和演示业务场景，再完成阿里云最小只读采集验证与模型工具调用验证。确认数据可得、模型能力可用后，再确定技术栈和详细接口设计。

本轮补充了技术背景调研：结合 Go 高于 Python、高于 Node.js 的熟悉度与未来 TUI 需求，建议先验证 Go + Eino，Python + PydanticAI 作为备选；云接入建议以云防火墙为核心，有限补充 ECS/安全组。上述仍是候选建议，未完成选型或实际接口验证。

## 已可运行的最小离线验证

详见 [实验说明](experiments/eino-offline/README.md)。当前仅是模拟模型驱动的集成验证，不是正式系统命令。

```bash
# 首次需要联网下载固定依赖；不修改全局 Go 配置。
bash scripts/eino-offline.sh prepare
# 准备后关闭模块联网执行，不需要模型密钥或云账号。
bash scripts/eino-offline.sh test
bash scripts/eino-offline.sh run
# 可选的资料完整性校验：Python 标准库，不联网。
python3 scripts/check_references.py
```

## 填写模型服务配置

本地在仓库根目录 `.env.local` 填写 `CFW_MODEL_BASE_URL`、`CFW_MODEL_API_KEY` 和 `CFW_MODEL_NAME`；该文件被 Git 忽略。可分享的空白模板见 [`.env.example`](.env.example)，详细规则见 [实验配置说明](experiments/eino-offline/README.md)。

```bash
# 仅检查并输出脱敏摘要，不发送模型请求。
bash scripts/eino-offline.sh config-check
```

当前配置入口已实现，但真实模型适配器和调用验证尚未完成；填写配置不会使离线样例自动访问付费服务。

已实测 Go 1.26.10、Eino v0.9.22。正式 Web、真实模型和云采集的安装/运行命令尚未实现，不用实验命令代替。

许可证沿用仓库已有的 [Apache License 2.0](LICENSE)。
