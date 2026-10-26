# 基于大模型智能体的云防火墙策略审计与推荐系统设计与实现

本仓库用于论文研究与系统实现。系统以云防火墙策略审计为业务主线，以大模型 Agent 为实现方法，围绕云资产、访问控制策略和业务访问需求，完成风险识别、证据补查、配置推荐与审计报告生成。

当前处于 **P2 系统设计阶段：需求 v0.6／B2 已于 2026-10-26 获用户确认，现有设计 v0.2 需按 B2 补充并通过 G2**。按用户“交叉审核无关键问题后进入设计”的条件授权，需求 v0.5／B1 此前作为设计输入；本轮用户明确“需求算过了”，据此采用 B2，不是用户沉默或技术验证代替批准。正式业务系统仍未实现。已有 Go + Eino 与两个 MiniMax 小样例仅为前期证据，不证明完整协议、审计效果或云端接入。

本轮（2026-10-26）新增 [出向／NAT 范围复审与原型 v0.2](docs/24-scope-expansion-and-prototype-review.md)：用户已确认需求 v0.6／PRD v0.4，**将 CR-1 的有限出向／NAT ACL 纳入 B2；正式视觉／组件、架构与技术取舍仍待 G2**。

## 已确认的方向

- 论文面向本科毕业或一般硕士工程实践的工作量，重视完整实现与可验证结果，不追求复杂算法创新。
- 首个适配云平台为阿里云，演示使用云防火墙和 ECS；真实环境计划集中开通、集中验证。
- 首版只读取配置、开展审计和提供推荐；由人确认并在云平台手动应用策略。
- 不接入云安全中心、不依赖其客户端或采集；轻量标注结合 ECS／安全组自动线索、候选推断和用户确认。
- 优先实现前后端分离的 Web 系统；CLI 作为可选后续入口。
- 模型接入优先兼容 OpenAI 接口规范；当前设计建议非流式 Chat Completions 与已有模型配置，正式协议／框架采用仍待 G2。
- 云防火墙业务与 Agent 工程并行设计。用审计结果体现系统价值，用工具选择、证据补查和执行记录体现 Agent 的实际作用。

## 已确认的首版需求范围（B2）

B2 保留互联网入向闭环，并纳入有限互联网出向和 NAT 边界 ACL；不扩展为完整 SNAT／DNAT 转换审计、跨云／跨 VPC 可达矩阵。统一流程为：读取公网资产和防护状态 → 关联访问控制策略与业务意图 → 执行规则检查和 Agent 补查 → 生成有依据的推荐与报告 → 人工修改后重新审计。

同时支持离线快照和真实只读采集。离线模式用于开发和重复实验，真实模式用于验证云平台接入；实际字段、产品版本和账号能力在后续真实只读验收核实，不阻止当前设计文档。

## 文档导航

| 文档 | 用途 |
| --- | --- |
| [项目定位与论文主线](docs/01-project-overview.md) | 研究问题、双主线关系、论文结构与贡献边界 |
| [论文开题报告与文献资料](docs/thesis-proposal/README.md) | Markdown正文、15篇文献导读与审阅记录；不维护Word／PDF副本 |
| [系统需求规格说明书 v0.6／B2](docs/02-requirements-and-scope.md) | 需求已确认；继承 B1 并纳入有限出向／NAT，设计待 G2 |
| [审计领域需求 v0.6／B2 附件](docs/12-audit-domain-requirements.md) | 保留 B1 语义，补充 CR-1 分边界判据与资料缺口要求 |
| [业务价值与资产画像](docs/13-business-value-and-asset-profiles.md) | 行业与 AI 背景、人工／标签／指纹方案、角色与组件的条件建议 |
| [安全基线与规则目录](docs/14-security-baselines-and-rule-catalog.md) | NIST／厂商主题映射、CHK-01～12、优先级、日志边界和新增验收案例 |
| [v0.3 历史自审](docs/15-requirements-self-review.md) | 自审发现、文档修正、离线检查与仍待用户审核事项；不是批准或业务测试 |
| [产品 PRD v0.4 与使用流程](docs/16-product-requirements-and-user-flows.md) | 五个轻量视图、自动证据与对话确认、审阅复审、失败分支和验收追溯 |
| [交叉审核与 B0](docs/17-requirements-cross-review-and-design-baseline.md) | 22 项问题／风险处置、设计默认与条件授权边界 |
| [数据与 API 设计](docs/18-data-and-api-design.md) | 版本、确认事务、任务幂等、私有数据、错误及报告契约 |
| [规则与 Agent 设计](docs/19-audit-and-agent-design.md) | 三值匹配／区间计算、工具与输出边界、预算和验证矩阵 |
| [会话历史与数据时序](docs/20-session-history-and-data-freshness.md) | 持续对话、框架恢复边界、缓存／刷新、累计耗时与分层反馈 |
| [凭据与专用工具认证](docs/21-credentials-and-tool-auth-design.md) | 启动／环境／STS／角色、期望账号、SDK 与 MCP 边界 |
| [跨入口推荐引导](docs/22-guidance-and-multi-entry-design.md) | Web／CLI／TUI 共用短语、空态／续聊／失败下一步和副作用 |
| [出向／NAT 范围复审与原型改进](docs/24-scope-expansion-and-prototype-review.md) | CR-1／B2 确认记录、联动文档、设计工具与原型验证边界 |
| [v0.2 Review 与发布核对](docs/23-design-v02-review-and-publication.md) | 迭代检查、修复、回归、隐私／署名和发布授权边界 |
| [离线交互原型 v0.2](docs/prototypes/audit-workflow/index.html) | 人工数据交互演示，不接 API／模型，不是正式前端；[说明](docs/prototypes/audit-workflow/README.md) |
| [系统设计说明书 v0.2](docs/03-architecture-discussion.md) | G2 待审：技术取舍、4+1 视图、模块、部署、安全和追溯 |
| [实验与演示方案](docs/04-evaluation-and-demo.md) | 对照实验、标注、真实云环境验证与费用管理 |
| [阶段计划与决策记录](docs/05-roadmap-and-decisions.md) | 已确认事项、未定问题、实施顺序与下一次讨论重点 |
| [官方资料与核实记录](docs/06-references.md) | 接口及产品资料来源、核实边界 |
| [技术背景与选型评估](docs/07-technical-background-and-options.md) | Go/Python/TS、Agent 框架、未来 TUI 与代表性论文/项目 |
| [云平台接入与能力映射](docs/08-cloud-integration-and-capability-map.md) | 分层只读 API、实际字段、支持边界与离线资料缓存 |
| [离线验证记录](docs/09-offline-validation-record.md) | 固定依赖、实际通过的检查及未验证范围 |
| [换环境开发交接](docs/10-environment-handoff.md) | 当前进度、迁移边界、新环境恢复步骤与下一任务 |
| [MiniMax 真实模型初测](docs/11-live-model-validation.md) | 4 次真实请求、两个案例、保护措施与未验证范围 |
| [离线参考资料](docs/references/README.md) | 11 篇官方正文＋1 模型／19 需求／9 初版设计／10 修订／8 范围及原型摘要，附来源与校验清单 |

## 开始实现前

仓库协作约定见 [AGENTS.md](AGENTS.md)。

按用户要求执行两道审核门槛：**需求主文档＋必要领域需求 → 用户确认需求基线（G1）→ 系统设计 → 用户审核设计（G2）→ 正式业务实现**。本轮已完成交叉审核并按条件授权进入设计；本轮已确认需求 B2，下一步联动补充系统设计并审核 G2，不是业务编码或追加真实调用。用户本轮授权普通推送当前本项目累计提交，包括此前仅本地保存的开题稿与原型修订；不强推、不改历史。

公开文档和前期验证已支撑 B0 与设计稿；真实云联调是否纳入最终验收与接入深度仍列为待审项，不要求现在调用。资料缺口及需要用户确认的业务范围见两份需求文档。当前不自动追加模型或云 API 请求。

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

配置入口与非流式 Chat Completions 实验适配器已实现；默认离线样例和 `config-check` 仍不发真实请求。MiniMax 初测的准确范围见 [记录](docs/11-live-model-validation.md)，不能泛化为所有协议或模型均兼容。

## 显式真实模型实验

仅在已获授权并确认单次/整组预算时执行；输入仍是人工样例，不连接云账号：

```bash
bash scripts/eino-offline.sh live -provider minimax -allow-paid-requests -scenario whitelist
```

`live` 必须显式允许请求；每次命令独立计数，不是全账户或跨命令总预算。当前只支持非流式 Chat Completions，Responses 明确拒绝。详见 [实验说明](experiments/eino-offline/README.md)。

已实测 Go 1.26.10、Eino v0.9.22 与模型组件 v0.1.13。正式 Web 和云采集命令尚未实现，不用实验命令代替。

许可证沿用仓库已有的 [Apache License 2.0](LICENSE)。
