# 三组评价的私有 Go 适配器

本包实现评价计划中的 `rules_only`、`fixed_model_pipeline`、`agent_tool_selection` 编排和独立评分契约。它没有公开 API、产品页面或命令，不创建授权、不认领生产队列、不采云、不替代应用服务的作业接受流程。代码和本地人工 HTTP fixture 通过不等于正式评价、真实模型兼容或真实云验收。

## 使用边界

1. 事前固定 `Protocol`：独立案例、调试／正式分割、重复次数、共同 brief、实际模型配置引用、采样、实际 resolved workload、固定工具遍历步骤和版本。`DatasetUse=formal` 要求 20～30 个 formal 案例及已声明的人工标签复核引用；这个校验不能替代真实人工复核。
2. `FreezeCase` 对脱离调用者可变对象的输入运行一次实际规则，保存输入／共同规则摘要。标签内容没有进入 `FrozenCase` 或 Runner 的字段。不同组不得重新调整该输入或共同规则结果。
3. 正常应用流程先批准并接受独立的 audit 作业，再由调用者提供已 claim 的作业 ID。每组使用自己的作业，引用同一个原有持久 grant。模型两组绑定同一个实际 `modelprotocol.Factory` 和采样；纯规则组不初始化模型。每次接受／claim 后及时执行，不提前 claim 一整套长实验来绕过作业截止时间。
4. `NewRunner(Binding{...})` 注入原 Store、Factory、RuntimeRevision、BudgetRef、公开安全的 ModelConfigRef、采样、已知敏感值和 Journal。`Run` 或 `RunSerial` 只处理显式传入的作业，不授予新权限。输入、范围、模式、运行配置、workload 或原有 grant 不一致时，模型发送前拒绝。
5. `RunSerial` 可以执行一次显式子集；全部运行结束后，`Aggregate` 才接受完整的 `case × arm × repetition` 矩阵。缺项、重复作业／运行／槽位、非终态或条件不一致会拒绝汇总。执行错误作为运行记录保留；Journal 失效会停止后续运行。

生产任务若需要实时云采集、准备任务或分批父子作业，应先在正常产品流程中完成并封存输入，再使用本包比较固定数据上的三种编排。当前适配器拒绝这些仍有云副作用的计划，避免把采集路径或数据年龄差异记成模型收益。

## 三组共同与不同之处

三组通过原 Store 的 `SealInput`、`SaveJobAudit` 和报告终结保存同一共同规则结果。模型两组共用原九工具 QueryService、最小字段投影、最终业务校验和实际受 Manager 管控的模型协议适配器。采样包装器不覆盖私有模型名、512 等更严输出 token 配置，也不产生新的 HTTP 额度。

固定模型组先按冻结步骤读取已经可用的引用，再做一次最终模型请求；预取记为工具步数，来源为 `predefined_pipeline`。它不伪造 assistant/tool 消息，不让模型追加查询，也不按案例标签选择步骤。Agent 组运行原 Eino 有界 Runner，来源为 `model`。两组记录真实 call ID、被实际服务接受的输入引用、前序工具依赖、证据和截断；追踪不会额外调用 BeginTurn 或提前授予引用。

固定预取可能在首次模型请求前失败。既有产品 AgentContent 要求“有工具计数即至少一次模型调用”，本包不为满足这个约束伪造 ModelCalls=1。该路径准确保留零次模型调用、实际预取路径、共同规则结果和 partial 终态；模型结果／报告可能尚未产生，以记录中的引用为准。

## Journal、用量和恢复

`OpenFileJournal` 接受显式私有文件路径，使用 0600 文件、排他锁、逐行追加与 Sync。序列化内容只有运行标识、版本摘要、受控计数和错误、工具引用、产物引用；没有原始输入、标签、模型自由文案、wire 或 reasoning。目录仍应放在仓库忽略的私有实验路径；不读取 dotenv 或机器 profile。

首次网络请求前必须保存 start。相同 run ID／请求摘要的已结束记录直接重放元数据；不同摘要拒绝。重启发现有 start 无 end 时先追加 `interrupted`，将产物存在性、逻辑计数、路径、耗时及用量标为 unknown，不能从这个 start 断言“没有产物／零次调用”。随后 Runner 仅从原 Store 同事务读取作业、审计、Agent、报告发布和物理账本，核对原输入／规则摘要后追加 `reconcile`：已提交产物恢复真实终态与引用，AgentContent 存在时恢复其实际逻辑／工具计数，原 interruption 行保留。没有 AgentContent 时不从 HTTP 数反推逻辑次数；无法恢复的完整工具依赖与阶段耗时保持 unknown。没有任何持久进展时仍为 interrupted／usage unknown；不封存新输入、不补报告、不自动恢复网络调用。关闭模型后仍可本地重放；新任务进入模型阶段才检查 Factory，未配置会明确保留规则并标记 partial。

`Recovery` 的 presence／counter 状态是恢复记录的必要判读条件；未知状态下兼容布尔 `output_absent=false` 不表示已证明存在产物，计数整数 0 不表示已知零。`Aggregate` 分别统计 unknown 产物存在性、逻辑计数、路径和耗时，保留 journal interruption 数量，不把恢复后的 completed 产物继续计成 absent。应用作业自身的恢复仍由原 Store／服务负责。畸形、未完整换行、超长或不符合本包规范的日志拒绝打开，不能只挑可读行计算成功率。正常元数据重放不复制模型内容；恢复对账读取到的受保护输出可交独立 scorer，日志本身没有这些文案。

物理尝试和 token 来自原 `BudgetUsage`，逻辑调用／预取工具次数另计。无 usage 响应保留 UnknownUsageAttempts，缺失 token 保持 nil；不把 logical calls 当成已计费请求，不推导金额。`output_absent` 表示没有已保存的业务输出，`model_output_absent` 单独表示没有合格模型输出；规则输出存在不能掩盖模型失败。

## 独立评分

`LabelSet` 仅交给 `NewScorer`，必须绑定同一协议摘要和标签版本。formal 协议的每个冻结 case 至少有一项显式标签（unknown／待确认／不适用也可以）；缺少任一 case 的标签会拒绝，不能凭整体 human_reviewed 声明为未标注案例生成正式评分。development 可以局部评分，未标注输出仍单列。规则事实按 `case + subject + service + frozen problem family` 聚合，相关检查不重复计数。已知正例没有检出记 FN，无法判定的输出另记 abstention；未知／待确认／不适用标签不混入确定正负样本。分母为零的比率为 `not_applicable`、Value=nil。共同规则层评分一次，三组不能声称不同规则准确率。

`ScoreModel` 默认 `pending_independent_review`。只有独立填写的 `ModelReview` 才分别记录模型断言、错误确定结论、保持待确认，以及每条建议的对象、证据、业务约束、无越权开放四项。建议四项允许 satisfied／not_satisfied／unknown；缺一条建议的评价会拒绝。推断未声明完整复核时维持部分复核；格式校验、模型自评、用户采纳和工具路径长短都不自动产生质量分。

## 已验证与未执行

本目录测试使用实际 audit、九工具服务、Eino、modelprotocol、Manager 和纯 Go SQLite；网络仅访问 httptest 的 loopback 人工协议服务。覆盖三组共享输入／规则／grant、固定预取与模型自选依赖路径、私有零额度、512 token 保留、失败／取消／截止、未知用量、Journal 失效／已有产物恢复／HTTP 后 Agent 保存前中断／重启不重发、关闭模型仍可重放、标签隔离和 formal 全案例覆盖边界。恢复时的未知字段进入统计，独立反例保留在相应 `independent_*_test.go`。

这些 fixture 的模型回答和 scorer 单测标签是人工测试材料，尤其单测中的故意相反标签不能当成评价真值。20～30 个独立正式案例、冻结人工标签及真实复核、正式三组运行、真实模型质量和费用／整改复审结果仍未执行。
