# E3受限Agent本机实现与限定验证记录

日期：2026-10-28。状态：**E3限定离线验证通过**。正式业务链、真实Eino Runner与人工协议模型的本机验证已完成；机器可读状态见[e3-local.json](e3-local.json)。需求B2、设计v0.6的G1／G2状态不变，前序[E0](e0-local.md)／[E1](e1-local.md)／[E2](e2-local.md)证据不回写。

## 当前实现

| 范围 | 实际行为与代码落点 |
| --- | --- |
| 显式迁移 | business schema 3增加预算策略、物理尝试及可空usage、Agent产品结果；旧版或缺必要表／usage列的非完整库拒启，普通打开不AutoMigrate。见[schema3](../../../server/internal/storage/schema3.go)及[回归](../../../server/internal/storage/schema3_test.go)。 |
| 正式Eino执行 | 每任务新建ChatModelAgent／Runner、模型与工具闭包；审计／助手分别受次数、时间、字节与单批上限约束。同批call ID先整体校验，成功返回引用到下一轮才晋升，不借批内顺序冒充依赖。见[Runner](../../../server/internal/agentrun/runner.go)及[Query契约回归](../../../server/internal/agentrun/runner_query_contract_test.go)。 |
| 固定资料工具 | 九个工具仅查询任务固定输入与可选原始规则结果；按范围、版本、字段投影与证据记账。初始／分区上下文保留evaluation_at、collected_at及受控一致性；IP／云ID、自由描述及例外理由不外发。未采集安全组明确not_collected。见[agentquery](../../../server/internal/agentquery/service.go)、[时点回归](../../../server/internal/agentquery/time_context_test.go)。 |
| 会话与结果 | ContextBuilder以接受消息位置、plan／turn／scope取最多20条／24 KiB获准历史，保存排除／截断计数；跨范围和旧wire／reasoning不回放。产品输出、本地绑定和固定manifest持久化前重验；归档／换scope不改已接受任务范围，原始wire只存运行内存。见[上下文](../../../server/internal/app/agent_context.go)、[产品结果](../../../server/internal/storage/agent_results.go)、[消息生命周期回归](../../../server/internal/storage/message_lifecycle_test.go)。 |
| 输出约束 | 角色为待确认候选；四类建议要求合法对象、原发现／同服务确认访问和已返回证据。显式think／analysis／reasoning_content及wire字段拒绝，普通依据解释允许；不能覆盖原规则判定／严重程度／证据。见[最终校验](../../../server/internal/agentquery/validation.go)、[标记回归](../../../server/internal/agentquery/review_reasoning_test.go)。 |
| 共享物理准入 | 物理HTTP先在短事务按应用／会话／任务预算预记，成功提交后才可发送；等待结束再查执行／取消状态，关闸后零发送。未知送达和预记后未发均保守占用，不自动退款或重放；新会话／重启／换密钥不重置同周期额度。见[账本](../../../server/internal/storage/budgets.go)、[outbound说明](../../../server/internal/outbound/README.md)。 |
| 用量与阶段 | 逻辑模型／工具次数与账本物理尝试分开；usage缺失保持unknown，显式零不丢，部分报告不当全量，聚合overflow使reported_tokens为空并保留已知／未知计数。模型阶段completed不等job终态completed，后续仍可cancelled／interrupted。见[用量HTTP回归](../../../server/internal/storage/budgets_usage_http_test.go)、[固定报告](../../../server/internal/report/agent_types.go)。 |
| 模型配置 | 正式服务只读取显式--model-config JSON和api_key_env指定的环境变量；无dotenv／默认profile／实验私配兜底。--config-check只本地摘要，不打开业务库或工作器、不DNS／HTTP。启动／预览零外呼；接受计划或明确发送后才可有界请求。configured／enabled不等认证、兼容或付费授权，原MiniMax模型和512等更严私配不变。见[配置](../../../server/internal/modelconfig/config.go)、[运行时](../../../server/internal/app/model_runtime.go)。 |
| API与Web | 任务结果新增snapshot_id、agent_result、attempt_usage；GET jobs/{id}/input读取任务sealed固定输入。Web分开显示规则、完整经校验解释、候选／条件建议／本地证据、用量与job状态；证据不跟随当前快照变化。实际Swagger由固定Swag统一生成并完成41路径／49操作双向对账。见[实际API](../../api/README.md)。 |
| 公开报告 | JSON／Markdown独立Agent层只投影固定模板、公共目标／证据别名、计数及受控错误，不复制模型自由文案／本地pointer／私有映射；不改变原规则与基础别名。受保护任务结果仍保存完整经校验解释。见[报告实现](../../../server/internal/report/agent.go)及[回归](../../../server/internal/report/agent_review_test.go)。 |
| 持久事件 | app sink将真实Runner工具回调保存为tool／call_id／status／elapsed_ms／evidence_refs，结果提交后stage_changed可含agent_result_ref；事件只恢复观察，不伪造工具／token流或重置额度。见[事件schema](../../api/events.schema.json)。 |

## 实际验证结果

| 项目 | 状态／结果 |
| --- | --- |
| Go全量测试、无CGO构建与vet | 287个顶层／678个含子项测试通过，0失败；go vet ./...与CGO_ENABLED=0构建通过 |
| 前端单元回归、类型检查与生产构建 | 51／51测试通过，类型检查与生产构建通过 |
| Swag生成与路由／方法双向对账 | 固定v1.16.6实际再生成Swagger JSON／YAML；41条路径／49项操作，TestGeneratedSwaggerMatchesActualRoutes通过 |
| 真实提交事件与当前schema核对 | 57条实际持久事件从SQLite e0_events和API分页分别导出，全部通过Draft202012 schema校验，覆盖八类事件 |
| 本机浏览器／服务联调 | Chromium 155.0.8059.12，26组检查通过；1440／768／390px；5任务共11次本机模型HTTP，云0／真实上游0 |
| 独立Review及修复复验 | 已确认问题修复并纳入最终全量；独立应用Review未发现新增实质阻断，文档Review措辞问题已修 |
| 文档链接／结构／JSON／需求追溯 | 静态文档／来源检查、JSON及JSON Schema结构、diff检查通过；不代表业务／运行验收 |

可重复检查命令沿用仓库现有入口：

```bash
bash scripts/e0.sh test
bash scripts/e0.sh build
bash scripts/e0.sh api
python3 scripts/check_design_docs.py
python3 scripts/check_references.py
```

前端命令、显式迁移和配置检查分别见[后端](../../../server/README.md)／[Web](../../../web/README.md)。这些命令是本轮实现的复现入口，实际范围以本表与下述局限为准。私有数据库、浏览器fixture状态和本地执行产物留在忽略目录，不公开口令、端点或真实配置。

## 浏览器实际链路与Review回归

5个持久任务为入向审计、NAT审计、普通助手、未知用量助手和无效输出助手。物理本机模型HTTP分别为2／3／2／2／2，共11次；前四项按预期completed，无效输出按预期failed／AGENT_OUTPUT_INVALID。每次都经过正式任务／Store／Eino／QueryService／HTTP账本，模型内容和usage由人工脚本提供，不能当供应商token或账单。

- 审计前人工确认的application覆盖进入sealed输入；后来确认background后，旧审计固定字段和L2的9 pass／7 fail／5 not_applicable保持不变。
- NAT脚本驱动资产上下文→关联证据两次工具查询，实际call ID不同；工具事件使用真实tool字段。该路径验证跨轮关联工程约束，不证明模型自主选择。
- 助手公开消息为持久经校验解释，获准历史纳入数按轮次3／5／7增加。证据展开实际按需GET本任务sealed输入；普通历史浏览和候选表单打开均零POST，不自动创建草稿。
- 未报告usage的任务保持reported_tokens=null，界面显示未知而非零token；无效输出无解释／候选，也未增加origin=model公开消息，数量保持3。
- 1440／768／390px均无横向溢出；检查中pageerror、浏览器跨origin HTTP和localStorage／sessionStorage条目均为0。

截图13张及完整浏览器／事件结果保存在忽略路径output/playwright下，不公开临时访问口令或原始数据库。临时fixture服务在20分钟后正常自动退出，token／state已清理。最终Go回归亦包含助手取消最终化、当前消息别名投影和health阶段元数据等修复；health已从E2改为E3，先取得对应红灯后修绿。

## 证明范围与未覆盖项

本次E3中的所有模型均为**in-memory人工模型或loopback人工脚本协议fixture**。它们驱动正式Eino、QueryService、持久化、HTTP准入及Web链，用于验证指定成功／失败路径；工具选择和回复由人工脚本预置。loopback HTTP虽计入相同物理账本，却不是上游MiniMax服务请求，in-memory模型也不产生上游物理用量。不能由此声称模型自主选择质量、真实连续兼容、审计效果提升或费用准确性。

尚未验收：所配上游MiniMax多轮工具／reasoning连续性与条件输出、真实CFW／ECS／NAT字段／分页／身份／权限、20～30独立正式案例与三组公平比较、至少一次真实人工整改→重采集→复审、2 CPU／4 GiB标准性能，以及所有目标浏览器／长时代理异常矩阵。E3不新增真实云或上游模型调用，也不使用两个历史小样例扩大结论。

E4真实接入尚未实现／验收；仍需先推进可离线完成的模型／云字段与协议适配、fixtures和失败回归，再按明确账号／地域／资产／权限／数据／预算执行真实只读验证。当前不能提前表述为“只剩外部阻塞”。云端写入、云安全中心、多云／多Agent平台与自动整改保持不在范围。
