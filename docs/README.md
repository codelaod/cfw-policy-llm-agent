# 项目文档导航

**当前基线：需求v0.6／B2和整体设计v0.6已通过（G1／G2，2026-10-27）。** [E0依赖契约](validation/evidence/e0-local.md)和[E1离线核心／持久闭环](validation/evidence/e1-local.md)已有实际证据；[E2持久服务／五视图](validation/evidence/e2-local.md)已通过限定离线验证；[E3受限Agent](validation/evidence/e3-local.md)已通过限定离线验证；[E4只读适配切片](validation/evidence/e4-adapters-local.md)已通过限定离线适配验证。真实接入、正式评价与完整业务验收仍未完成。

2026-10-30正式代码整改已有限定本机结果，限定结果见[代码阶段证据](validation/evidence/conversation-implementation-local.md)及实施计划§2.3；完整正式客户旅程验收及真实取证仍待执行。此前复核已在现行正文补记配置来源迁移、对话意图／资源解析、自动证据、M0缓存与参数校准缺口。客户流程看[PRD](requirements/product.md)及[唯一场景目录](requirements/customer-journeys.json)，方案取舍看[官方接口工作负载研究](research/collection-workload.md)与[架构](design/architecture.md)，实际代码缺口看[逐项实现台账](development/implementation-gap.md)。本轮原型与方案的实际核对结果见[客户流程记录](validation/evidence/product-workflow-local.md)。后续按[当前整改顺序](development/implementation-plan.md)；历史协议／浏览器证据保持原范围，不等于产品主入口完成。

## 1. 推荐阅读

1. [项目定位](project/overview.md)与[项目决策](project/decisions.md)：范围、批准状态和已选路线。
2. [系统需求](requirements/system.md)→[领域需求](requirements/audit-domain.md)／[规则目录](requirements/rule-catalog.md)→[PRD](requirements/product.md)。
3. [总体架构](design/architecture.md)，再按任务查数据／API、审计／Agent、会话、权限、云采集和前端设计。
4. [实施计划](development/implementation-plan.md)→[验证清单](validation/verification-plan.md)／[评价计划](validation/evaluation-plan.md)。

## 2. 按文档类型查找

| 类型 | 目录／主文档 | 维护内容 |
| --- | --- | --- |
| 项目定位与决策 | [project/](project/) | 一份当前状态与关键批准记录，不堆逐轮review |
| 需求与产品 | [requirements/](requirements/) | 系统、领域、规则、业务画像和使用流程；只说明要做什么及验收边界 |
| 系统设计 | [design/](design/) | 已批准的架构与详细契约；小并发/API增补已经并入对应正文 |
| 研发与交接 | [development/](development/) | E0～E5入口／退出条件、环境恢复、交付物与权限边界 |
| 验证与证据 | [validation/](validation/) | 验证计划、评价方法、追溯、人工契约样例与真实执行记录分开 |
| 已实现API | [api/](api/README.md) | 当前实际生成Swagger JSON／YAML与八类事件schema；只记录已注册接口 |
| 技术调研 | [research/](research/) | 选型依据、替代方案和云能力资料，不作为第二套待选技术栈 |
| 一手来源 | [references/](references/README.md) | 来源编号、核读摘要、日期和哈希清单 |
| 论文 | [thesis-proposal/](thesis-proposal/README.md) | 开题正文、正式题录、阅读指南与一份编写说明 |
| 原型 | [prototypes/](prototypes/audit-workflow/README.md) | 人工数据交互参考，不是正式前端／规则／Agent或真实SSE |

## 3. 设计文档职责

| 文档 | 看什么 |
| --- | --- |
| [architecture](design/architecture.md) | 模块、部署、数据流、3会话、普通SQLite WAL与性能目标 |
| [data-and-api](design/data-and-api.md) | 身份／版本、事务、任务、接口、审阅／报告、Swag生成保留 |
| [audit-and-agent](design/audit-and-agent.md) | 三值规则、证据、受限工具、模型能力与建议校验 |
| [session-and-execution](design/session-and-execution.md) | 连续会话、时效策略、排队／预算、恢复与并发 |
| [credentials-and-permissions](design/credentials-and-permissions.md) | 应用／云／模型凭据、显式身份、外呼准入与范围 |
| [guidance-and-entrypoints](design/guidance-and-entrypoints.md) | 五视图、情境下一步、Web及后续终端入口语义 |
| [aliyun-collector](design/aliyun-collector.md) | 互联网入向／有限出向／NAT分区、只读API、分页与完整性 |
| [frontend-and-events](design/frontend-and-events.md) | 组件／路由、鉴权fetch SSE、seq补发、轮询与错误恢复 |

## 4. 文档治理约定

- 每项事实／决策只在所属正文维护；其他文档引用，不再建立“第N轮review”“vX审核包”平行入口。
- 修订结论并入需求／设计；未决执行项进实施或验证计划；实际运行结果进入evidence，不把计划写成通过。
- 变更需求范围、核心技术或验收目标仍须明确记录；本次分类整理不改变业务基线。
- 只维护现行正文；旧版本与审批过程使用Git追溯，不另建批准文件快照或专用哈希机制。
- “业务待确认”“未实现／未实测”保留真实含义，不属于未完成G1／G2审批。
- 新接口文档在实现后放`docs/api/`，由固定工具生成并保留；正式业务接口与E0隔离诊断接口按实际注册生成，未实现接口不放占位规范。
- 文档改动检查链接、JSON、需求对应关系与引用；涉及原型文案时运行现有离线状态回归，不冒称业务验收。
