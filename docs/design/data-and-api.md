# 数据、任务与Web API设计

基线：需求v0.6／B2、设计v0.6，G1／G2已通过。本文为该领域的现行详细设计；实际运行验收另行取证。

E0已实现独立的`/api/v1/e0/*`人工联调子集，并生成[实际规范](../api/README.md)。本篇的正式业务实体／路由仍按后续阶段实现；不得把人工任务入口替代计划消费或业务确认。

E1已实现输入、原始规则／证据、审阅、固定公开报告和配置复审的Go应用／持久层，限定结果见[E1记录](../validation/evidence/e1-local.md)。业务HTTP、会话与确认流程在E2接入；API规范只记录实际已注册路由。

## 1. 身份、版本与来源

- 内部对象使用后端生成的不透明 ID。云对象另保留 provider、账号标识、地域、资源类型、原始资源 ID；不能只按名称／标签／公网 IP 关联。
- 同机多服务分别有稳定的 `service_id`；角色、必要访问、允许集合与例外按服务及所审范围关联，而非整机唯一标签。
- 请求意图、已封存实际输入、快照、画像／业务／基线版本与报告不可变；需刷新时先存请求／计划，在规则前只封存一次实际输入。当前版本指针与本地审阅状态可变，但有修订号／历史。
- 来源由服务端写入：受控采集为 `live_readonly`，内置公开构造样例为 `synthetic`，普通导入为 `imported`。上传文件自称真实／完整不产生可信标记，声明来源另保存为未核实内容。
- 时间以带明确时区的 RFC 3339 输入并规范化保存，界面按用户时区展示；fresh_after 为已声明变更完成后的采集下限，未来／无时区或矛盾值明确拒绝，不排等待未来日期的无限刷新。分析就绪时固定 `evaluation_at`、采集窗口、规范化版本与默认行为来源；明确复验可指定原值。重复规则测试使用同一评估时点；报告始终说明基于哪个时点的配置，不能直接声称当前数据面已生效。

### 1.1 B2 的策略范围身份

`policy_scope_ref` 是服务端生成的范围引用，对应 provider／已核对账号引用／控制面配置／boundary／direction／实际策略集／IPv4 子域。仅支持 internet/in、internet/out、nat/out；NAT 策略集绑定一个明确网关。资源地域与 SDK 控制面地域分开，账号编号内部用字符串保留。

每条规则、引用、证据、业务要求、输入清单和报告携带分区归属；唯一性不能只靠 AclUuid。请求中的 scope 引用必须属于当前 workspace／connection 的获准目录。scope 由服务端显式允许的边界／网关配置或已授权导入／采集结果产生，未知能力不会触发目录自动发现。后端校验实际 owner，不能以客户端传的 direction、MemberUid、API 名或网关名称作为授权依据。

选择模式为 `assets`（1～50 个明确资产）或 NAT 专用 `gateway_policy`（恰好一个网关，可以没有资产）。assets 列表须无重复且原始长度在上限内，不能去重后绕过数量保护。资产空列表不是全部资产；网关模式只执行网关适用项，资产级规则仍需可定位的关联。具体键与厂商字段映射见 [云采集设计](aliyun-collector.md)。

## 2. 逻辑实体与主要字段

这是数据契约而非最终 DDL。JSON 结构需版本化并受长度／数量约束，SQLite 用外键与检查约束维护必要一致性；每个连接显式启用并验证 foreign_keys，不能以 ORM 声明关系代替实际约束。迁移在 G2 后实现。

| 实体 | 核心内容 | 不变性／约束 |
| --- | --- | --- |
| `audit_workspaces` | 默认空间、来源／范围与资料引用 | 不引入组织／多租户 |
| `conversations` | 标题、范围、归档、当前上下文摘要引用 | 多轮复用，归档不删证据 |
| `conversation_context_summaries` | 会话＋规范分区／字段投影版本、覆盖消息位置、结构化公共摘要 | 不把其他分区的历史或旧工具片段混入当前外发上下文 |
| `conversation_messages` | 序号、用户／助手公开正文／事件、scope 归属、引用、建议版本与时间 | 不存凭据／reasoning，不孤立重放旧 tool 消息 |
| `conversation_turns` | 用户消息、执行、公开答复关系及状态／幂等／上下文 | 与消息／任务事务绑定，同会话一个未结束执行槽位 |
| `collection_policies` | 来源／范围、enabled、周期／时效／next_due | 默认关，只采集数据、不自动调模型 |
| `data_sources` | 标签、厂商、允许地域／范围、凭据引用、连接状态 | 不保存到公共 DTO 的密钥；连接由服务端配置，不接受任意 URL |
| `policy_scopes` | 服务端范围引用、规范分区键、来源配置版本、允许对象／动作 | 无实时隐式发现；无跨账号／网关自动扩展 |
| `execution_plans` | 规范请求、预估数据计划、效果上限、配置／资料版本、摘要、到期和消费引用 | 本地预览不外呼；内部助手计划只来自明确发送；创建任务时原子验证并消费，同键重试返回原任务 |
| `nat_associations` | 网关／防火墙／资产地址、观察位置、来源、窗口、evidenced／candidate／unknown／conflict | 关系证据不是可达性；转换条目不混成 ACL |
| `snapshots` | 来源、范围、采集窗口、schema／normalize 版本、内容摘要、完整性概况 | 创建后不可修改，普通导入不能伪装受控真实采集 |
| `snapshot_sections` | 数据族／API、原采集窗口／复用来源、分页、完整性／时效、错误 | 成功空集、缺权限、漏页、截断和未知分开；无总数不填零 |
| `snapshot_items` | 对象类型、内部身份、原资源身份、规范化内容、原始证据引用 | 同快照与分区不可变；高级条件保留，不被无声删除 |
| `asset_profile_versions` | 资产／服务、角色、环境／应用、重要性、来源和确认 | 多角色可用；候选不在此自动生成确认版 |
| `business_intent_versions` | 服务／发起资产、方向／分区、必要访问、允许上界、完整性、明确拒绝、例外及有效期 | 角色确认不自动生成授权；明确禁止和未知不同 |
| `asset_heads` | 当前画像／业务版本指针、聚合修订号 | 条件更新；任何已确认字段变动增加修订号 |
| `profile_drafts` | 资产集合、旧基版本、结构化候选、用户修订、有限公共对话摘要、状态 | 不存完整模型／思考消息；批量预览逐资产列差异 |
| `confirmation_previews` | 草稿修订号、目标基版本、规范化差异、可确认部分、摘要 | 一次确认对应一份有效预览；改变草稿需重新预览，无需再次调用模型 |
| `baseline_versions` | 启用检查、风险端口／模板、语义支持、例外政策、版本 | 模型只读；B0 的 M1 默认未启用 |
| `jobs` | 会话／消息引用、计划引用／请求／sealed输入、policy_scope_ref、阶段／耗时／预算与取消 | 请求先固定，准备后实际输入只封存一次；不隐式重复执行 |
| `job_events` | schema_version、job_id＋严格递增 seq、type、occurred_at、受控 payload／证据引用 | 状态／引用与事件同事务、提交后发送；终态最后；SSE 与轮询同源，不存 Eino 原始消息／reasoning，见[前端与事件](frontend-and-events.md) |
| `evidence` | 来源类别、同版本对象引用、字段路径／受控片段、时间与摘要 | 引用受快照／业务／基线版本约束；不向客户端返回可任意读的文件路径 |
| `findings` | origin、检查／问题族、资产服务、verdict、assessment_basis、severity、remediation_priority、范围证据 | 原始规则发现不可被模型或审阅删除；模型疑点另 origin，不能覆盖规则风险分布 |
| `recommendations` | kind、policy／policy_set／protection_scope／asset_service 目标、可选 finding_id、条件／影响、来源保证和校验状态 | 无云写动作，不存可自动执行的通用脚本 |
| `review_actions` | 发现／任务引用、comment／plan_change、意见、采纳／暂缓与理由、修订、幂等和保存时间 | 追加历史；不改变原始风险判定；不能由模型自动创建用户审阅 |
| `review_heads` | 发现／建议的当前本地计划与 review_revision | 与追加 action 同事务 CAS；不是原始规则状态 |
| `reports` | job 输入／结果版本、本地处理头版本、公开投影版本、格式／内容摘要 | 每次生成固定版本；新审阅记录不原地改写旧报告 |

`evidence` 的来源可以是快照字段、已确认业务要求或基线条目；来源类型与对应外键必须一致且属于当前任务允许的版本和规范分区，不接受模型随意拼出的路径。

原始 API／导入正文、数据库及私有版本映射位于 `data/private/` 等既有忽略目录。文件名由服务端生成，原始响应不含授权请求头；公开资料、构造样例与真实数据分开。

SQLite常规连接池下，仓储在序列化／网络响应前关闭行游标，事务内不再通过 DB 另开查询连接；事务对象显式贯穿同一原子操作。网络、模型、规范化大计算在事务外完成，写入／状态更新短事务化；有界等待超时返回明确错误，不无限卡住事件读取。SSE 订阅不得跨网络写入／心跳持有数据库连接、事务或行游标；通知仅唤醒短读，不是事件事实源。

事务是业务原子提交边界，WAL是日志模式选择。本文件沿用[架构设计](architecture.md)的“WAL＋普通共享连接池＋短事务”；不强制单写连接／读写分池，多会话正常提交读写，由SQLite协调写锁。

仓储采用 GORM＋纯 Go libtnb/sqlite／modernc，E0已构建和验证人工任务子集。Updates(struct) 可能跳过零值，确认必须显式列字段／map；false／空集合不能省略。禁用通用 Save 覆盖不可变版本，以修订 WHERE＋RowsAffected 检查竞争，使用同一 Tx，不把网络／模型等待放事务里。不在启动时隐式 AutoMigrate；SQL 日志不输出历史正文／参数。

纯 Go 是硬约束：G2 后验证 CGO_ENABLED=0、驱动／JSON／bool、外键、事务和重启；不为 ORM 回退 CGO。适配不满足则通过仓储接口回退直接 modernc，不改变业务历史。

## 3. 快照与完整性契约

必需分区按任务和检查决定，例如当前分区身份、CFW／NAT 控制面状态、该方向／网关 ACL、引用对象及适用的 ECS／安全组上下文。安全组缺口影响画像，不一定阻止已有完整 CFW 分区上的其他检查；CFW 漏页则不能跳过潜在前置规则后给出确定允许／拒绝。

采集记录真实请求过滤器和控制面参数，区分资产所处地域与厂商接口的控制面地域；不凭同名 Region 字段假设等价。读取 CFW 策略不只过滤 accept；必要的 drop／状态／高级条件都要保留。分页按对应接口版本的契约耗尽，计数矛盾、截断或限额触发均标部分读取。

`discover_region` 与 `explicit_ids` 是 collect 的资源发现选择器，不是 audit 的 selection.kind。审计只接受已明确的 assets 或 gateway_policy；先发现再选对象，不通过空资产列表在审计中隐式发现全账号。空 ID 列表不隐式变成全部账号；显式列表预先超过上限直接拒绝，发现过程中达到上限则记录截断和部分范围。发现模式仅在用户明确选定的有限地域内执行，达到上限就停止并说明，不能静默扩大范围。所选方向／网关分区的全部相关规则仍需收齐，否则受影响检查降级。

为证明匹配完整性，采集范围可能需要读取所选账号整个所选方向／网关策略分区；这要在采集前明确展示，而不是把“审计几台资产”误解成只读几条 allow。区分云读取范围、所审资产范围和模型外发范围：模型仅能获取与所选端点／发现关联的证据，不因同一快照含其他策略而获得全账号阅读权。

离线输入首版只接受有界 JSON，不支持 ZIP、任意路径、外部 `$ref`、URL 拉取或可执行模板。结构／深度／数量校验不等于证明文件真实；所有未识别的匹配条件保留或降级，不默认为 ANY。

缓存合格与完整性分开，保留原数据族时点；每个分区的 CFW 策略／保护／引用按一个配置族收集或复用，不把本次创建时间给旧事实续新。三种数据策略见[会话与执行](session-and-execution.md)。

### 3.1 枚举完整性与观察一致性独立

`snapshot_sections` 同时记录 `enumeration_complete`、`observation_consistency`、原始采集窗口、过滤与上限。后者至少为 `observed_stable / changed / unverified / fixed_artifact`；fixed_artifact 仅表示离线制品固定，不表示云端全量；其余也不是 API 事务快照标志。完整但两轮配置不同，不能当成合格当前配置；返回字段缺失也不能因总数为零而通过。

配置族封存包含适用的 owner 元数据、全相关 ACL、静态引用与 provider_semantics；默认最多两轮完整复读，规则条件／顺序／引用／状态指纹一致才记录 observed_stable，命中统计不参与配置指纹。缺少第二轮或观察变化时保留证据、标局限，受影响路径检查不能输出通过。实际策略与预算见[会话与执行](session-and-execution.md)／[云采集设计](aliyun-collector.md)。

导入可以保存用户关于时间／完整性的声明，但来源可信度仍为 imported；不能伪装 live_readonly 或由上传 JSON 自行批准 provider_semantics。内置 synthetic 样例的设定与实际产品状态严格分开。

## 4. 画像、业务授权和确认

### 4.1 业务字段

- 角色：`unknown / candidate / confirmed / conflict` 等可见状态由来源与当前版本决定；拒绝草稿是事件，不隐式删除旧确认版。
- `role_claims` 与 `service_bindings` 可多条；端口可未知，不能因为候选数据库就自动确认 3306。
- `required_access` 表示必要访问；`allowed_access` 表示允许上界，`allowed_set_complete=false` 表示非完整声明。
- 空列表且不完整＝未声明，不能推导拒绝全部。完整空集合仅在用户明确声明“禁止该所审入向访问／该分区的新发起外联”并确认适用方向后成立；UI 和服务端分别核对这一明确否定。
- 例外有对象／服务、来源、理由、适用范围与到期；“暂缓”不是例外，角色确认也不是例外批准。

### 4.2 入向与出向业务字段不是反向复用

- 入向以固定目标资产／服务为中心，比较允许来源、协议和服务端口。
- 出向以固定发起资产／服务为中心，`dependency_targets` 表示经确认的外部目标、协议、目标端口、用途及来源；在支持子域内形成 `required_access` 和完整／非完整的 `allowed_access`。
- NAT 的业务记录还绑定所审网关／分区及地址观察点的证据。未证实关联时不把某条 ACL 自动归为某台 ECS；网关策略集可以独立审查，但不能假称资产路径已知。
- 每份授权版本明确适用的 scope；从其他方向／网关复制只产生新草稿，必须重新预览确认。角色、历史流量、现有策略和 DNS 观察不是授权来源。
- 允许上界已声明完整时，必要访问必须是其子集；明确 deny_all 与非空必要访问冲突则预览拒绝。未声明完整时，不能用“未列出目标”自动生成拒绝或违规。

完整出向表单属于 G2 后正式 Web 的 IntentEditor，不因原型有一个出向样例就认定表单已交付。

### 4.3 确认事务

1. 草稿保留创建时的资产聚合修订、画像和业务基版本。对话／表单修改只更新草稿修订。
2. 预览由服务端规范化并校验，返回差异、错误／未知、`preview_id` 与可确认部分；前端展示角色与授权是不同部分。
3. 确认提交预览、草稿版本条件和明确的 `confirmed_sections`。仅确认角色不应用授权差异。
4. 事务内再查所有目标基版本；任一冲突返回 409，批量不做半数落地。成功只为用户确认的部分创建新版本、增加头修订并将草稿置已确认。
5. 同幂等键的确认重试返回同结果，不反复生成版本。旧预览、改变后的草稿或越界对象不能直接确认。

批量角色操作仅应用明确选择的共同字段，逐资产预览；访问授权不从安全组批量复制。默认受信标签映射为空；若将来配置映射，须有已批准范围与规则版本。

## 5. 连续会话、任务与幂等

业务会话保存可见消息与被引用资料／报告；任务是执行边界。kind 为 collect、assistant_turn（画像引导／审计追问）、audit、connection_check。历史解释不自动重收集，新消息不丢前轮确认，旧答不当当前事实。

发送带 client_message_id，用户消息、轮次、幂等和排队执行一次事务提交。同会话已有 queued／running／cancelling 执行时明确等待／取消，不自动抢占长期审计；结果完成追加公开答复与引用，中断保留历史。ContextBuilder 取同空间且当前分区／外发投影获准的近期消息、结构化摘要与固定资料，裁剪范围有记录，不存／回放完整模型 wire message 或 reasoning。

### 5.1 统一准入、预览与明确发送

所有有界任务使用同一个 `ExecutionAdmission`：身份／空间 → kind 对应允许效果 → 分区／对象 → 固定资料／配置版本 → 字段投影 → 预算／队列／幂等。kind 是任务类型，不是权限提升开关。所有实际外部尝试必须引用已接受的执行意图。

`POST /execution-plans` 仅做本地预览，取代 v0.3 草稿中的 audit-plans 路径；没有已部署 API 需要迁移。计划建议 5 分钟有效，返回规范请求、预计复用／刷新、缺口、外部效果上限和更严服务端预算，不获取 STS 或调用云／模型。

| kind | 允许的外部效果 | 进入执行的明确行为 |
| --- | --- | --- |
| audit | 按数据策略可能读云；按 mode 可能调用模型 | 用户接受计划，/jobs 仅提交 plan_id |
| collect | 白名单云／凭据查询，无模型 | 用户接受只读采集计划；可选定时仅使用明确启用的采集授权 |
| connection_check | 有界身份／产品只读探测，无模型 | 用户接受检查计划；GET 状态和 config-check 仍零外呼 |
| assistant_turn | 仅模型与本地固定资料工具，云调用上限为零 | 用户明确发送且界面显示当前范围／模型效果；服务端原子形成并消费内部计划，不由模型或推荐自动启动 |

公开计划的 kind 仅为 audit／collect／connection_check，采用分类型 DTO：audit 固定审计对象，collect 使用明确的资源发现选择器，connection_check 仅检查所选连接。未知／越界字段返回 422。assistant_turn 计划只由消息处理器根据已认证操作者的明确发送创建，authorization_origin 由服务端赋值，不信任客户端或模型自称“已确认”。

普通聊天不用每条消息再弹一个审计确认框，但发送必须显式 `model_use_accepted=true`、绑定当前 scope／资料和字段投影。配置密钥、打开页面、点击推荐或保存草稿均不构成发送授权。资料不足时助手返回采集计划入口，不在助手预算内偷偷刷新云；旧报告解释固定旧版本。

prefer_fresh 预览须说明排队后可能刷新；pinned_snapshot 云读取上限为零。`POST /jobs` 只消费 plan_id，kind／scope／mode／预算从服务端计划读取，不接受客户端覆盖。事务内核对期限、资料／配置、容量和幂等；同键重试先返回原任务，已消费计划换新键为 409 PLAN_CONSUMED。内部助手计划不开放给 /jobs 重复消费。

POST messages 在授权、参数或队列检查失败时不半提交用户消息和任务；界面保留输入并标未发送。正常路径将消息、轮次、内部计划／请求清单和任务一次事务落地。同会话未结束执行不被新消息自动抢占；本地历史读取、确认和审阅不占执行槽位。

### 5.2 请求与实际输入

request_manifest 受理即固定会话／意图、scope、资料基版、数据策略、配置／外发政策和预算；data_plan 区分拟复用／刷新与原因。准备完成、规则开始前只封存一次 resolved_input_manifest，固定实际快照／各族原时点和 evaluation_at，之后不切最新。

需刷新时 audit 在自身阶段调用采集服务，不排子 collect 后等待同工作器；合格既有快照可复用，同范围后台请求可被本次产物满足。根预算覆盖所有阶段，L1 缓存预览不是最终输入。

### 5.3 状态与幂等

queued→running→completed／partial／failed／cancelled；queued 取消直接 cancelled，运行中取消才置 cancelling。独占进程锁取得后才能把上次遗留运行中置 interrupted，不自动重放。completed 是流程结束而非整体安全。历史保留后用户可继续新轮次，不是恢复上次付费请求。

初版一个长任务工作器、队列 10，前台优先、定时默认关。后台使用服务根 context＋任务剩余时间，不保留 Gin 请求 context 或池化对象。总时间／阶段上限以 20 为准。

持久 jobs 是队列事实来源，容量／会话槽位与插入同事务；内存通知仅用于唤醒。排队过期在凭据解析前落 failed／QUEUE_DEADLINE_EXCEEDED，零外呼；状态终结释放会话槽位。

同幂等键同内容返回原对象、异内容 409，键／摘要／消息／轮次／任务同一短事务及唯一约束；防止客户端重发，不保证供应商网络重试计费恰一次。尝试预记并计失败，共用任务／会话／应用更严限额，会话汇总不是货币账单，新会话不能清空应用已用量。

## 6. 拟议 API 清单

前缀 `/api/v1`。所有数据／任务接口需要本地应用访问保护，云／模型凭据不进入这些 DTO。列表有界分页；响应可带 request_id／版本，不输出原始错误正文。

| 方法与路径 | 作用 | 关键契约 |
| --- | --- | --- |
| GET `/workspaces/current` | 默认空间与当前会话引用 | 不引入组织平台 |
| GET／POST `/conversations`、GET／PATCH `/conversations/{id}` | 新建／重开／归档会话 | 不是每条消息新建，版本和范围受控 |
| GET／POST `/conversations/{id}/messages` | 历史分页／明确发送 | client_message_id＋当前资料／scope＋model_use_accepted；统一准入后消息与有界助手任务原子保存 |
| GET `/guidance` | 多入口推荐短语和下一步 | 本地查询，不调用云／模型／STS |
| GET `/data-sources/{id}/auth-status` | 已有脱敏连接状态 | 配置不等身份验证，不在 GET 中换 STS |
| GET／PATCH `/collection-policies/{id}` | 本地周期／时效设置 | 可选默认关，开启要有来源／范围，不自动调模型 |
| GET `/capabilities` | 脱敏模型／来源／功能状态 | 仅已配置、模式、限制／能力，不给密钥／实际端点 |
| GET `/policy-scopes` | 当前连接已有的范围目录、缓存能力／缺口 | 纯本地，不因列出 NAT 选项就扫描账号；未知能力明确显示 |
| POST `/execution-plans` | audit／collect／connection_check 的统一预览 | kind 为公开支持的有限枚举；本地无外呼；固定 scope／对象／效果上限／配置版本 |
| GET `/data-sources` | 选择服务端已配置来源 | 标签／范围／只读就绪状态，不在线提交云密钥 |
| POST `/snapshots/import` | 有界 JSON 导入 | 服务端设置来源类型；结构失败 422／体积超限 413；不解析外部引用 |
| GET `/snapshots`、`/snapshots/{id}` | 快照列表和完整性 | 时间、范围、分区、计数和限制；不返回私有文件路径 |
| GET `/snapshots/{id}/assets`、`/snapshots/{id}/assets/{asset_id}` | 范围内资产与当前上下文 | 不透明 ID、关联证据／候选缺口；原始身份仅授权内部页面 |
| GET `/snapshots/{id}/policies`、`/snapshots/{id}/security-groups/{group_id}` | 当前快照的配置证据 | 稳定分页和高级条件，不是任意云 RPC |
| POST `/jobs` | 消费用户接受的执行计划 | 只传 plan_id；kind／scope／预算来自服务端，Idempotency-Key 同事务，202 返回 job_id |
| GET `/jobs/{id}`、`/jobs/{id}/events` | 状态和有界事件页 | 状态带同读快照 view_seq；事件 seq>after_seq，最多 100 条，next_seq／has_more／last_seq；见[前端与事件](frontend-and-events.md) |
| GET `/jobs/{id}/events/stream` | fetch SSE 订阅与补发 | 同应用访问头；after_seq、text/event-stream；终态尾部 204；只读、不隐式执行或取消，见[前端与事件](frontend-and-events.md) |
| POST `/jobs/{id}/cancel` | 请求取消 | 终态不能恢复；合作停止与上游撤回区别明确 |
| GET `/jobs/{id}/result` | 结果及覆盖 | 响应带同读快照 view_seq；运行中返回 is_final=false、completed_phases 与 L2；事实、模型补充和阶段缺口分开 |
| POST `/profile-drafts`、PATCH `/profile-drafts/{id}` | 人工／对话共用草稿 | 源自当前可访问对象；PATCH 带草稿修订条件 |
| POST `/profile-drafts/{id}/preview` | 无模型的规范化差异 | 返回 preview_id／草稿修订／基版本／可确认部分，未知不自动补全 |
| POST `/profile-drafts/{id}/confirm` | 本地确认 | preview_id＋expected_draft_revision＋明确确认部分＋幂等；409 冲突无部分写入 |
| POST `/profile-drafts/{id}/reject` | 拒绝候选 | 不删除旧确认，不自动转未知授权 |
| GET／POST `/findings/{id}/review-actions` | 读取审阅历史／追加意见与本地计划 | expected_review_revision＋幂等；成功返回 action_id 和新 revision；失败不提示已保存 |
| POST `/reports` | 从结果及审阅头生成固定导出版本 | Markdown／JSON，默认公开白名单投影，无模型调用 |
| GET `/reports/{id}`、`/reports/{id}/content` | 元信息与受保护下载 | 前端带访问头获取后本地保存，口令不放下载 URL |
| POST `/audits/{job_id}/rechecks` | 用新快照／新资料新建审计 | 相同幂等规则；服务端对比范围与版本，不修改旧报告 |

外发授权不足／模型未配置时，候选任务返回可回人工的错误；审计可显式选仅规则，或在请求 Agent 模式后保留规则并标模型阶段未执行／部分完成，不静默冒充完整 Agent。

### 6.1 人工示例：审计预览请求与启动

```json
{
  "kind": "audit",
  "conversation_id": "conv_demo_01",
  "data_source_id": "source_demo",
  "policy_scope_ref": "scope_demo_internet_out",
  "selection": {"kind": "assets", "asset_ids": ["asset_demo_a"]},
  "data_policy": {"mode": "prefer_fresh", "allow_stale_fallback": false},
  "profile_version_ids": ["profile_demo_v2"],
  "intent_version_ids": ["intent_demo_out_v1"],
  "baseline_version_id": "baseline_b2_v1",
  "mode": "rules_and_agent",
  "disclosure_policy_id": "projection_demo_minimal"
}
```

这是发给 `/execution-plans` 的人工示意；返回计划后，另以 `POST /jobs` 的 `{ "plan_id": "plan_demo_01" }` 和 Idempotency-Key 显式启动。请求先固定，实际快照和默认 evaluation_at 在分析就绪后封存；明确复验可复用原时点。pinned 必须用 `data_policy.snapshot_id` 指定已有同分区版本；force_refresh 可指定 `fresh_after`，不拿旧缓存装新结果。互相矛盾的数据策略字段返回 422。前端不能任意增预算或指定模型 URL／工具；此示例不是已存在 API。

### 6.2 人工示例：确认草稿

```json
{
  "preview_id": "preview_demo_03",
  "expected_draft_revision": 3,
  "confirmed_sections": ["roles"]
}
```

动作请求使用 JSON 中的 `expected_draft_revision` 与 Idempotency-Key；预览服务端已绑定各资产基版本。此例只确认角色，来源／端口授权没有被顺带确认，不能自动进入完整允许集合。

公开导出体在事务外有界生成，B0 建议直接作为受限内容存 SQLite，和报告元信息／幂等引用一起短事务提交，避免另引文件事务。原始采集正文先安全落地再提交引用；崩溃留下的未引用文件不提供下载，不通过猜路径恢复，更不自动删除其他用户文件。

## 7. 错误与客户端行为

| HTTP／类别 | 含义 | 客户端动作 |
| --- | --- | --- |
| 400／422 `INPUT_INVALID`、`UNSUPPORTED_INPUT`、`SNAPSHOT_SCOPE_MISMATCH` | 结构／状态／范围不合法或能力不足 | 展示字段／范围与手工／离线入口，不把错误当空结果 |
| 401 `APP_ACCESS_REQUIRED` | 应用访问口令缺失／不匹配 | 本地解锁入口；不是要求输入云／模型密钥 |
| 403 `SCOPE_DENIED`、`DISCLOSURE_DENIED` | 越界对象或未获准数据 | 保持原任务，不能由模型提升权限 |
| 409 `VERSION_CONFLICT`、`IDEMPOTENCY_CONFLICT`、`TURN_IN_PROGRESS`、`PLAN_STALE`、`PLAN_CONSUMED`、`REVIEW_VERSION_CONFLICT` | 草稿／目标变化、同键异内容、同会话活动轮次或计划过期／配置改变 | 重新取差异预览；不自动换新键覆盖 |
| 413／429 `INPUT_TOO_LARGE`、`QUEUE_LIMIT` | 体积／队列／调用限制 | 缩小范围或等待，不后台无限提交 |
| 503 `STORAGE_BUSY`、`STORAGE_UNAVAILABLE` | 本地短事务忙／存储不可写；不是供应商故障 | 保留草稿与原幂等键，先查询已受理任务；不可写时停止新外呼，不能提示已保存 |
| 作业错误 `COLLECTION_PARTIAL`、`COLLECTION_SCOPE_MISMATCH`、`CONFIG_CHANGED`、`MODEL_FAILED`、`OUTPUT_INVALID` | 阶段失败／内容无效 | 显示部分结果与未执行范围，用户决定新任务 |
| 作业错误 `BUDGET_EXHAUSTED`、`JOB_INTERRUPTED`、`QUEUE_DEADLINE_EXCEEDED` | 次数／时间耗尽或重启中断 | 停止后续请求，保留尝试和证据，不自动重放 |

公开错误包仅含稳定 code、中文摘要、request_id、受影响阶段和是否允许用户重试；不得带密钥片段、原始供应商正文、私有 URL 或完整模型标识。

## 8. 报告与复审一致性

报告固定 job 输入／结果版本及生成时审阅头。公开导出使用报告内别名替代原资源／IP／证据位置；模板生成的规则解释和已验证结构可以输出，模型／用户自由文本默认不公开。所有模式均禁止导出凭据和内部推理。

比较单位为规范分区键、稳定资产服务／网关策略对象、观察点和预定义问题族，保留原始各检查结果。新旧快照对象缺失、身份／方向／网关／观察点／业务／规则／时点不一致时说明可比性；不能把未采到、未检查或暂缓算作修复。变更已下发／真实业务恢复属于额外实际验证，不由本层配置比较证明。

## 9. G2 后需要完成的契约测试

RC-17 的条件更新／批量原子性和显式空集合；RC-18 的重复点击／网络超时／幂等冲突；RC-19 的请求账本／重启不重放；RC-20 的访问、范围和字段投影；导入来源伪造、路径引用、分页中断和大响应；取消后不再请求；旧报告和新资料独立。

这些是完整契约要求。E1已执行输入／结果／审阅／报告／复审的离线子集；会话、确认、正式任务API及真实准入仍需后续阶段取证。

历史、缓存、累计耗时、专用认证和跨入口引导验收见RC-21～31、AC-15～20。GORM与有限持久实体已有E1证据；完整会话／调度／业务接口继续按计划实现，源配置仍由服务端注入。

## 10. B2 示例与新增契约验证

[人工设计示例 JSON](../validation/fixtures/audit-contracts.json) 给出范围目录、预览请求、效果上限、已封存输入、关联状态和跨分区拒绝条件。所有数据是人为设定，不是 API 返回或已实现 schema。

设计／契约验证覆盖 B2-01～03、09、11～16、18；包括 scope 身份、空选择、字符串总数、相同 ACL ID、计划到期／重复消费、缓存族、旧报告与网关切换。具体矩阵见 [验证计划](../validation/verification-plan.md)。静态文档校验只检查示例结构／引用／预算一致性，不执行上述业务。

### 10.1 跨分区历史不得通过摘要绕过范围控制

同一会话可以保留多个边界的本地历史，但模型上下文按当前计划的规范分区和 disclosure 版本筛选。历史助手／工具衍生内容、业务授权和结构化摘要必须带 scope；其他分区只保留本地引用，不把自由文本原样外发，也不采用把两段摘要合并后再提示模型“忽略另一段”的做法。

仅明确标记为无事实的通用界面模板可跨 scope；未知归属的历史不自动外发。当前用户输入仍经过敏感值检查和投影提醒，不能宣称自然语言脱敏可识别所有秘密。解释旧报告时绑定旧报告 scope／版本，而不是拿当前页面的新 scope 随意拼历史。

示例 JSON 的 model_context_examples 演示同会话跨分区消息的保留／排除关系；静态校验器只校验示例关系，不表示真实 ContextBuilder 已实现。

## 11. 审阅与保存确认

意见与本地计划是已有 FR-16／AC-15 的持久业务记录，不只是页面标记。请求必须指向已落地的 finding／job 和当前 review_revision；即使 job 尚在运行或后来取消，已保存的审阅也不丢失。

- 动作为 comment 或 plan_change；comment 有非空意见，plan_change 为采纳待办／暂缓，暂缓须理由。意见不是业务授权／风险例外，模型不能代替用户创建审阅动作。
- JSON 明确 `expected_review_revision`，并带 Idempotency-Key。事务内验证引用／范围、同键请求摘要和当前修订，再追加 action、CAS 更新 head 和幂等结果；同键同内容先返回已提交的原 action（不重新要求当前 head 仍等于旧修订）；只有新键请求才检查当前 head，新键旧修订返回 409 REVIEW_VERSION_CONFLICT，不覆盖别人的意见。
- 只有提交成功后的响应包含 action_id、review_revision、saved_at 才显示“已保存”；存储失败、超时、409 都保留草稿并标未保存／待核对。超时可先刷新服务端历史，再用原键核对／重试，不能换键重复追加。
- GET history 从服务端分页返回，刷新／重启可查；UI 不能靠 localStorage 冒充持久化。SQL 日志与公开报告不带未授权自由文本；原始规则发现保持不可变。
- 报告生成固定已选的 review_revision；后来增加意见不修改旧报告，可显式生成新报告版本，生成报告不调用模型或云。

动作型修改（确认、审阅、会话／草稿编辑）统一使用 JSON 的 expected_revision 类字段做业务 CAS，并以 409 表示业务冲突，不把它冒充 HTTP If-Match。若以后采用真实 ETag／If-Match，须另按 RFC 9110 的条件请求语义实现和测试（条件失败涉及 412），不能继续把它等同于业务 CAS 的 409；依据见 D4-01。

## 12. 输入覆盖与结论保证分开

同一规则内核接收三种来源，但来源不决定“允许忽略已知缺口”。增加 `assessment_basis` 与必需的 `assumptions`，和 verdict 分开保存、传给工具／UI／报告：

| 输入 | 可用的计算条件 | 对外表述 |
| --- | --- | --- |
| 内置 synthetic | 固定构造数据、完整性为明确案例设定，引用与语义均满足 | 只声称该人工案例中的结论，不证明真实云效果 |
| imported | 结构／范围／版本正确，声明覆盖完整且引用闭合；声明仍未核实 | 可计算“在所提供配置及完整性声明成立条件下”的集合结论；basis=imported_assumption，不能标 live_observed 或可信云全量 |
| live_readonly | 有实际请求与分区枚举／窗口证据，满足计算所需完整性／一致性 | 仅在该次观测范围与时点说明配置事实，不证明数据面实际可达 |

显式漏页、未知引用、未声明覆盖、互相矛盾的数量／scope 或相关语义不支持时，仍 insufficient_data／unsupported，不能用“导入假设”覆盖已知错误。上传的 source_kind／trusted 标识由服务器忽略或作为未核实声明保存，不能提升保证级别。

imported_assumption 的 pass／fail 只属于条件配置推导，界面必须显示“导入声明条件下”，报告置顶保留未核实条件；不能放进“已证实真实风险／当前安全”汇总。工具和建议输出必须保留 basis／assumptions，省略或升级保证级别即校验失败。来源真实性与配置集合运算分开，既不把导入当现场，也不让正常离线输入永久只能空报告。

## 13. 验收追溯与逻辑／物理结构

review_actions／heads、计划和尝试账本的事务顺序对应 V4-03／04／06／07；导入保证对应 V4-05；原有 DC／RC 与 B2 测试不删除。完整映射见 [验证计划](../validation/verification-plan.md)。

本节逻辑实体不是强制一实体一表；物理设计允许有界版本化 JSON，但不能因此丢失唯一键、外键、修订 CAS、同事务约束和可查询历史。禁止启动 AutoMigrate 隐式改变既有数据。正式 DDL／迁移与真实驱动行为在 G2 后完成，不以此文档假称数据库已验证。

### 13.1 结果维度与筛选契约

继承既有规则目录：verdict 表示通过／不符合／待确认／信息不足／不适用／不支持；severity 为高／中／低／信息，证据不足可未评定；remediation_priority 为 R1／R2／R3／V0。实现等级 M0／M1 属于规则／功能目录，不充当风险优先级。

API 独立提供这些字段和来源依据版本，前端按对象、角色、依据类型、判定状态、严重程度与整改顺序筛选；V0 缺口进入核实队列，不等于低风险。模型置信度不能直接提升规则 severity；模型疑点独立标识，处理计划与采纳状态仍是另一组字段。

## 14. 任务事件接口

正式事件采用 [29 第 4～6 节](frontend-and-events.md) 的 schema、连续 seq、页／流共用协议、非法游标 422、终态尾部 204 与 view_seq。提交事件前先提交对应业务结果／引用，终态 CAS 与 job_terminal 同事务；SSE 不发送未保存结果，不把心跳写入历史。任务执行 context 与订阅 context 分开，断线不取消任务，重连不消费计划或重放旧模型／工具消息。

所有 job kind 共用同一事件读取机制；执行权限仍只由原有准入授予。订阅端 401／403、429、网络重试和格式错误分开处理。首版保留完整可访问任务事件，不加入未设计的历史裁剪。此处为设计，不是已经存在的接口。

## 15. 接口规范生成与交付

### 15.1 推荐方案

以Go DTO／Handler注释为REST结构源，固定版本的**swaggo/swag**生成接口文档。官方主线说明的是Swagger 2.0（OpenAPI 2.0），不能直接标成OAS 3（D6-02）。E0验证选定版本与本项目结构兼容；本方案不安装或伪造生成结果。

实际工程建立后保留：

- `docs/api/swagger.json`、`docs/api/swagger.yaml`：生成后纳入Git，接口变化同步更新。
- `docs/api/README.md`：固定生成工具／版本和重复生成入口；`docs/api/CHANGELOG.md`记录兼容性变更。
- SSE的事件schema／人工示例：对应29的seq、事件类型与恢复语义，不假定普通Swagger生成器能自动表达整个流状态机。

当前这些是目标产物路径，不是已经存在的正式规范。不手工维护一份与Go结构脱节的“第二套接口”；前端和后端共同使用生成契约，必要说明由设计文档补充。

### 15.2 必需内容与检查

覆盖实际method／path、字段类型与必需项、枚举／分页、请求头、幂等键、版本条件、202任务引用、错误码和人工示例。尤其说明SSE的访问头、after_seq、text/event-stream、终态／204、断线不取消和显式取消。

实现阶段检查：固定版本重新生成无意外差异；JSON／YAML一致；规范与实际路由双向对账；典型请求／响应和前端使用一致。文档浏览不触发云／模型，示例不含秘密。若提供交互文档入口，沿用同源应用访问保护，不另外开放公网匿名接口；默认不增加额外文档服务。

这是用户要求的研发交付物，不是新业务功能。现有Markdown接口表不等于已生成Swagger，生成成功也不等于接口业务测试通过。
