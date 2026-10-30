# 前端与任务事件流设计

基线：需求v0.6／B2、设计v0.6，G1／G2已通过。本文为该领域的现行详细设计；实际运行验收另行取证。

E0已建立Vue／Vite／Router／Nuxt UI最小联调台和真实本机SSE客户端，固定版本、实际结果与限制见[E0记录](../validation/evidence/e0-local.md)。E2已交付五个业务页面及正式离线API，限定结果见[E2记录](../validation/evidence/e2-local.md)；E3已接独立模型结果、候选／建议、固定证据和物理用量视图，已通过的限定离线与浏览器证据见[E3记录](../validation/evidence/e3-local.md)。下文完整异常矩阵与所有目标浏览器仍须分别取证。

2026-10-30纠偏：对话是业务主入口，五视图是资料、过程、发现与报告的辅助呈现。无snapshot也应能表达审计意图、检索／消歧对象并确认计划，不能先要求编辑后台资产JSON或选择快照才可发问。当前正式ConversationWorkspace已支持无snapshot的目标／澄清／proposal与接受；业务草稿／可信归属、任务／准备结果和分批段卡亦已接正式组件。限定离线浏览器验证不等于完整24旅程／88分支验收；剩余项与产品验收见[UF-13](../requirements/product.md)和[对话规划](guidance-and-entrypoints.md)。卡片展示解析依据和差异，确认沿用同一服务端意图／草稿版本，不以新增表单代替对话主链，也不让自由文本直接触发任意云API。

## 1. 选择与非目标

| 层次 | 本方案设计 | 边界／依据 |
| --- | --- | --- |
| 应用与路由 | Vue 3＋TypeScript＋Vite＋Vue Router | 五个业务工作区；不因使用 Nuxt UI 引入 Nuxt 应用框架、Nitro 或 BFF |
| 组件与样式 | Nuxt UI v4＋Tailwind CSS v4 | 单一组件体系，替换原 Element Plus 建议；官方有 Vue/Vite 入口（D5-01／02） |
| 客户端通信 | 共用 API 客户端；fetch SSE 事件订阅＋有界 GET 轮询 | 不是 Nuxt UI 内建的后端；发送、确认、取消仍走受保护 HTTP API |
| 后端 | Go＋Gin＋Eino、纯Go SQLite WAL、3任务槽位 | 无新增 Node 服务、消息中间件、多 Agent 或数据库引擎 |
| 模型协议 | 维持非流式 Chat Completions 设计 | SSE 推送任务事件不证明模型 token 流式／流式工具调用兼容；本方案不增加这项验收声明 |

Nuxt UI v4 官方 Vue 示例提供 Vite／Vue 插件、CSS 导入及 UApp，路由可接 Vue Router；本项目明确保留 Router，不使用 router:false 来自行实现页面切换。ChatMessages 的 messages/status 属于展示契约，可映射项目自己的公开消息，不必因官方示例出现 AI SDK 就引入其服务端协议（D5-01／03）。官方示例属于资料依据，实际构建与联调以各阶段执行记录为准。

框架小版本、Node／浏览器环境、锁文件在 G2 后 E0 固定，并验证实际 peer dependencies；不把移动的 v4 文档分支当锁定版本。Tailwind v4 的官方浏览器基线见 D5-06：Chrome 111／Safari 16.4／Firefox 128。项目演示使用受支持现代浏览器，具体实测版本须记录；不声称兼容旧内核。

## 2. 组件映射与视觉约束

| 页面／区域 | 组件候选 | 不能丢失的业务信息 |
| --- | --- | --- |
| 应用外壳 | UApp、DashboardGroup／Sidebar／Panel、NavigationMenu | 五个路由、当前范围、离线／真实来源、持续会话入口 |
| 工作台 | Table、Badge、Alert、Button | 资料时点、完整性与下一步；没有运行不显示健康分或风险为零 |
| 资产／业务确认 | Form／FormField、Input／Select、Checkbox、Modal | 事实、候选、已确认分开；先预览再确认；端口不自动成为业务授权 |
| 执行页 | 自定义阶段时间线＋Badge／Alert；ChatMessages／ChatPrompt 展示公开答复 | L0～L4、工具公开摘要、订阅状态、显式取消、部分结果 |
| 发现与证据 | Table／列表、分栏详情、Slideover／Accordion | 规则原始判定、模型疑点、条件、证据、影响、审阅保存结果分开 |
| 报告／复审 | 语义化报告版面＋Button／Alert | 固定版本、来源保证、未覆盖项、新旧可比性；不原地改旧报告 |

具体组件名称／props 以固定 v4 小版本实测为准，不复制模板中的业务、路由或服务端示例。先核对空白对话、消歧、业务确认、执行及失败恢复，再核对发现与固定报告；五视图辅助同一主链。原型更新见 [离线原型说明](../prototypes/audit-workflow/README.md)。

采用 neutral/slate 中性色、emerald 主操作、明确的风险与未知语义，统一 4/8/12/16/24/32px 间距、系统中文字体、表格密度与焦点样式。品牌绿色不作为“通过”标记；未知、失败、部分完成均必须有文字。界面不照搬演示模板，不混用 Element Plus；Tailwind 是同一体系的样式基础，不是第二套组件库。

图标采用本地 SVG 或构建时打包的固定集合；系统字体优先，不依赖运行时 CDN／远程字体／Iconify 查询。Nuxt UI 可配置图标 clientBundle，但必须用浏览器网络检查确认实际构建的离线启动行为（D5-01）。原型继续 CSP connect-src 'none'，只是本地 CSS 的视觉映射，**不是 Nuxt UI／Tailwind 已运行的验收**。

## 3. 三类流不能混为一谈

1. **业务事件流**：服务端保存的阶段、工具公开摘要、结果／消息引用 → 浏览器；本方案采用 SSE。
2. **模型生成流**：模型服务 → 后端的文本增量／工具参数片段；当前不采用、不伪造打字动画。
3. **Agent 编排**：Eino 的决策／工具调用 → 受权限约束的工具与事件投影；不向前端透传 Eino 原始消息或内部 reasoning。

上游非流式时仍可先展示已保存的规则发现、工具开始／结束、阶段和等待状态；公开助手消息整体保存后才发送 message_available。工具摘要、缺口与结果可以渐进出现，但不能把普通阶段提示标为模型思考全文。

## 4. 事件与读取契约

沿用[数据设计](data-and-api.md)的job_events。每个 job 的 seq 从 1 严格递增，唯一键为 (job_id, seq)；状态／结果引用与相应事件在同一短事务提交，提交后才发通知。终态和 job_terminal 事件同事务写入；job_terminal 后不再追加该任务执行事件。后续用户审阅有独立审阅历史，不改任务尾序号。

| 字段 | 约束 |
| --- | --- |
| schema_version | 本方案为 1；未知版本不得盲目跳过后继续推进游标 |
| job_id／seq | 必须属于当前受保护任务；seq 为安全整数范围内的正整数，SSE id 是其十进制文本 |
| type | job_status、stage_changed、tool_started、tool_finished、findings_available、message_available、report_available、job_terminal |
| occurred_at | 服务端保存时间，RFC 3339；不是浏览器收包时间或厂商 SLA |
| payload | 受类型约束的公开摘要、稳定引用和状态；不含凭据、原始上游错误／reasoning／自由路径或大段报告 |

```json
{
  "schema_version": 1,
  "job_id": "job_demo_stream_01",
  "seq": 3,
  "type": "findings_available",
  "occurred_at": "2026-10-27T10:00:03+08:00",
  "payload": {"phase": "L2", "result_ref": "result_demo_rules_01", "finding_count": 1, "is_final": false}
}
```

上例仅为人工协议示意，映射到 SSE 的 event 为 type，id 为 seq，data 为完整 JSON，空行结束一条消息。心跳使用注释行 `: heartbeat`，不分配 seq、不落 job_events、不计模型尝试。SSE 线格式依据 D5-04；并非 chunk 恰好等于一个事件。

| 方法与路径（均在 /api/v1 下） | 契约 |
| --- | --- |
| GET /jobs/{id}/events?after_seq=N&limit=100 | JSON 事件页，seq>N；最多 100 条；返回 events、next_seq、has_more、last_seq；同一次短读事务取得页与高水位，空页 next_seq=N |
| GET /jobs/{id}/events/stream?after_seq=N | fetch 发起，Accept: text/event-stream，沿用应用访问请求头；从 seq>N 补发后追尾；只读，不新建任务 |
| GET /jobs/{id} 与 /result | 权威任务／结果查询；响应携带 view_seq，正文与 view_seq 在同一短读快照取得；前端忽略较旧视图响应，按 job 隔离 |
| POST /jobs 与 /conversations/{id}/messages | 沿用计划消费／明确发送和幂等；不能为连接 SSE 自动重发 |
| POST /jobs/{id}/cancel | 唯一显式取消入口；断开订阅不调用它；保留已有取消／终态竞争约束 |

after_seq 是非负十进制安全整数；N 大于该任务 last_seq 或格式／数值越界为 422 INVALID_EVENT_CURSOR，不能伪装空结果。首版不自动清理仍可访问任务的事件，不引入无定义的过期游标恢复。浏览器没有完整本地投影时从 0 重建；不能把持久游标孤立保存后跳过消息。未来若保留策略需要裁剪，须另外设计同快照恢复接口，不能静默跳过已删历史。

初次读取状态不会推进事件游标。事件用于追加时间线和触发合并的权威状态刷新；收到事件后可读到较新状态，但旧事件不得回退当前状态。客户端仅在完整事件校验并成功处理后推进最后连续 seq；重复事件忽略，遇缺号则暂停前移，先用 JSON 事件页补齐，再恢复订阅。按任务切换时中止旧订阅并隔离迟到响应。

每条事件序列化上限 4 KiB，正文用稳定引用另取；超长公开摘要在保存前安全截断并标记 truncated，不截断 JSON 帧／关键引用。解析器未完成帧缓存上限 64 KiB；UTF-8 多字节、CRLF、跨 chunk、多条 data 行、注释与空行必须按格式处理。未知事件类型、畸形 JSON、schema 不支持时停止订阅并提示协议错误；不得吞错后宣称已同步。

当前E3实际工具载荷使用`tool`、`call_id`、`status`、`elapsed_ms`与`evidence_refs`，不使用旧草案的tool_name；开始／结束均来自真实Runner回调，未执行的工具不伪造事件。stage_changed在Agent／云产品结果提交后可分别含`agent_result_ref`／`cloud_result_ref`。字段／枚举以[实际事件schema](../api/events.schema.json)及app sink为准；工具内容另取受保护任务结果／sealed输入，不透传原始协议。

## 5. fetch 订阅、恢复与轮询兜底

原生 EventSource 构造参数没有自定义应用鉴权头入口（D5-04），因此本项目用 fetch 的响应流与 AbortController（D5-07），解析／恢复封装在一个 EventClient；是否采用轻量解析库在 E0 以固定版本评估，不引入第二套 AI 后端协议。

| 客户端状态 | 展示与行为 |
| --- | --- |
| connecting／live | 显示“连接中／实时更新”；最后同步时间与任务执行状态独立 |
| reconnecting | 显示“连接中断，任务未因此取消”；保留已收到的事件和结果 |
| polling | 明确“轮询兜底”；和 SSE 共用 seq、事件处理器和鉴权，不能双通道同时运行 |
| auth_required／forbidden | 401 要求重新解锁；403／404 停止订阅；不循环探测，也不索取云／模型凭据 |
| protocol_error | 422／未知协议／不合法帧明确失败；保留记录，重新加载记录只读且从 0 重建，不重建任务 |
| ended | 已处理终态并查询结果，或终态尾部返回 204；停止订阅与轮询，completed 仍不等于整体安全 |

网络失败或无心跳 45 秒可中止本次订阅，按 1／2／5／10 秒加小幅抖动退避；连续 3 次失败转 JSON 轮询，正常间隔初值 1.5 秒，失败退避 3／5／10 秒。429／503 尊重有上限的 Retry-After，不忙循环；需要鉴权／权限的错误不属于网络兜底。轮询模式先追完 has_more 的有界页，每批调度让出事件循环，且最多一个在途请求；终态必须在事件尾部排空后再停止。用户“恢复实时”先停止轮询，再从同一连续 seq 订阅，不发送执行类 POST。

页面隐藏可关闭订阅并保留内存记录，返回时从同一游标继续；关闭页面／切换路由只结束观察，不取消后台工作。刷新后重新解锁、读取历史，从 0 恢复，不能重放旧工具消息或恢复一次付费请求。EOF 未收到终态时按只读状态查询和重连处理，不能当审计完成。204 只用于已终态且 after_seq 到尾部的合法订阅；活动任务永不返回 204 来暗示完成。

## 6. Go／Gin 与 SQLite 实现约束

Gin 官方示例证明有 Stream／SSEvent 入口（D5-05），但本项目不照抄示例的广播／内存消息作为事实源。工作器继续使用服务根 context＋任务 deadline；SSE handler 只持本次订阅请求 context，r.Context().Done() 只释放订阅，不取消 job。

- job_events 是事实源，内存 channel 仅发送可丢弃的唤醒信号；订阅循环按 seq 分页读取并关闭行游标，之后才写网络。无新事件时至多每秒做一次短读补查，避免订阅建立竞态漏掉提交；没有跨网络等待的 SQLite 事务／连接。
- 每个可见任务最多一条前端订阅，服务初值最多 8 条并发订阅（含多标签页）；超限 429。不为首版增加 Redis、MQ 或全局长事务。
- `Content-Type: text/event-stream; charset=utf-8`，`Cache-Control: no-store`；逐帧 flush，关闭该路由响应压缩／代理缓冲（有代理时明确配置并实测）。心跳初值 15 秒，不虚构百分比。
- 用受支持的 net/http 流式写控制设置每次写截止 10 秒；单次订阅寿命上限 120 秒后只读重连；响应头明确订阅寿命，近期仍收到有效心跳且到达该寿命的正常轮换不累计为网络失败，避免第三次正常轮换误入兜底。SSE 不受普通 JSON handler 的短整包超时截断，也不通过关闭所有服务器超时规避；头部／读取／写入／停机限制分开测试。固定 Gin／Go 组合需验证 ResponseController／flush 支持；不支持则 E0 不通过。
- 慢客户端超过写期限即断开；有界批次最多 100 条，背压不拖住工作器、不丢已提交事件。服务重启按原有恢复流程写 interrupted 和终态事件；客户端重连看到中断，不自动续跑。
- 终态及报告／公开答复引用必须先落地，事件尾部补发完毕后关闭；取消与完成竞争沿用原子终态 CAS。写存储失败不能发“已保存／已完成”成功事件。

这些数值是项目工程约束，E0／E2／E3仅对实际记录的场景取证，不是上游产品保证；事件订阅的连接时限不延长／重置任务排队与执行总预算，重连也不增加外部调用额度。

## 7. 安全与原型边界

事件页、流、结果、报告均使用同一应用访问／范围检查；鉴权在发送 200／SSE 响应头之前完成。应用口令仅在浏览器内存与请求头，禁止放 URL／EventSource 查询参数／localStorage；不以本次调整改成 Cookie 登录或开放 CORS。持续订阅也有最大寿命；服务端配置轮换应关闭旧订阅或触发重新鉴权，旧口令不得无限期保留连接。

只输出公共白名单投影：工具名／受控摘要、阶段、时间和证据引用；资源描述按文本转义，不把 HTML／模型内容变成新工具或执行授权。使用聊天组件不等于启用不可信 Markdown HTML，富文本展示另经安全验收。

[原型](../prototypes/audit-workflow/index.html)由HTML／CSS／JS文件组成，保持无外呼、全部人工数据。订阅 live／断线／恢复／polling／终态均为内存模拟，刷新清空；同一页分别保存“模拟事件账本”和“已显示连续事件”，用于检查断线时不提前泄漏后续结果、恢复补发不重复、模型失败保留规则。不得把这些断言算成真实 SSE、Vue/Nuxt UI、数据库、规则或 Agent 验收。

## 8. 实现验证

依赖构建、真实传输和多会话事件验证统一见[验证计划](../validation/verification-plan.md)。当前原型不是正式组件／真实SSE验收。

## 9. 多会话与接口契约

执行改为3个任务槽位，每任务seq独立；切换／同时观察不同会话不得复用错误job_id或cursor。原最多8条SSE订阅限制不变，足以覆盖3个活动任务，不把订阅数等同于业务任务并发。订阅读页后关闭Rows／读事务归还连接，再写网络；不在SSE网络等待中持连接／事务，不阻塞正常读写或默认WAL检查点。

Swagger 2.0记录SSE路由／请求头／after_seq／状态和text/event-stream，事件联合类型及恢复状态机另保留JSON Schema及本文说明。3会话交叉事件、慢客户端隔离、终态与接口产物双向校验见[总体架构](architecture.md)。

## 10. 对话主链的状态与组件绑定

下列主链已由正式ConversationWorkspace及共享状态服务接通，空白对话不要求snapshot。界面消费服务端类型化状态，不靠前端正则拼云请求；完整M0仍按客户分支逐项验收。

| 状态／组件 | 必须展示与动作 | 绑定和失效规则 |
| --- | --- | --- |
| 空白／ChatPrompt | 示例、连接与模型独立状态，目标可先保存；规则模式立即生效 | 无snapshot仍可发送本地目标／澄清；识别秘密在保存和模型前阻断 |
| ResourceCandidates | 名称、地域、类型、ID、归属依据、目录时点；同名选择／补条件 | intent_revision＋catalog_revision；新ID不能被旧冲突地域误过滤 |
| Discovery／ImportPanel | 有限发现效果；导入格式／版本错误可重导，部分资料有缺口 | 保留原目标，导入声明不是真实云观察；不静默扫描账号 |
| AuditPlanPanel | 真实对象集合、分区、资料策略、模型模式、缺项与外部效果，明确开始 | proposal_id/revision，改变对象／方向／模式使旧卡失效；不显示原始JSON或让用户逐API配置 |
| Facts／ProfileDraft | 自动事实来源，候选与旧确认并列；接受、编辑、拒绝、未知 | 对象／服务／方向／基版绑定，切组或移除成员重新核验适用性 |
| ConfirmationDiff | 角色、允许、必要、完整性、例外分别可选 | 各section明确确认；例外有对象／检查／来源／理由／到期；未知不抹同句已知部分 |
| JobTimeline | 当前阶段、已用时间、公开工具目的与依赖证据、已得结果、取消 | stage与工具说明都可读；不是源码事件或内部reasoning |
| Findings／Report | 原始规则、模型补充、缺项、影响、本地审阅，固定Markdown／JSON导出 | 选中历史report_id，不取最新替代；partial／cancelled／interrupted各自标注 |

正式组件包括ConversationBusinessPanel（共用业务草稿／分项确认）、ConversationGroupPanel（可信归属预览／确认）、PreparationResults（本任务保存的候选与覆盖）及BatchResults（完整集合、逐段manifest／报告、covered／failed／unprocessed与已记录用量）。打开段对象／报告前只读核对父子关联，复审明确选择原段；取消作用于父任务。usage_unknown与已保存逻辑调用分别呈现，不能把记录零当实际零。受理回执未知时保存原会话／正文／幂等键，不因GET预览为空而猜成功，迟到读取／回执不串入另一会话。

分批父任务的batch_segment_started／batch_segment_finished／batch_request_stopped只促使读取最新已提交结果，不创建或继续执行。父级Guidance导航原任务的段列表与未处理集合，不能把父级无合并report误说成各段都没有报告。

同稿冲突页保留原输入并逐字段并列最新保存值，默认不选；显式rebase后只重新预览，角色／访问checkbox仍空。未知回执的原正文／键与当前更高修订分开；报告切换后迟到的生成／复审成功只保留其回执，不抢新页面、写错错误或解除新操作的busy。资产当前业务视图按固定snapshot明确集合读尽分页，超过首100对象不回退到旧确认。

审计页的计划预览与启动回执也按路由任务、会话与资料隔离，迟到成功仍保存任务回执，网络未知仍保留原正文／幂等键；不得抢回历史页或把旧错误显示在新任务。报告页复审接受卡展示服务端计划的原对象／分区、固定资料与时点、继承模式、云／模型效果及缺项；当前固定资料复审零新增云读取，是否使用模型按本计划效果明确显示。

用户变更当前目标只更新草稿；运行任务的对象／manifest固定，侧栏清楚区分“当前目标”和“正在执行的任务”。旧卡片使用自身绑定，不能作用于最新目标、任务或报告；跨会话切换中止旧订阅并隔离迟到响应。确认和审阅只有持久成功才已保存，未知受理先查原请求。

“现在”走prefer_fresh资格判断，显式“刚改完／刷新”走force_refresh；旧报告解释零云读取。模型disabled时rules_only任务按自身流程可completed；选择Agent但其阶段未完成才partial。cancelled／interrupted保留已得结果，文案不得称本次审计已完成。用户等待期间可查看先存规则、离页后返回，页面不要求逐阶段手动点击。

CloudResults在产品资料未就绪但有观察时显示“已保存部分读取记录／尚未形成可审计的完整资料”；ConversationTaskStatus与过程页均可只读查看，未封存时无资料选择或自动审计动作。identity_only预览不要求资产／网关；前端不复制100次／50资产／6次历史硬封，真正准入由服务端本次profile与私限校验。GDE-05历史模型解释保护核对A报告／任务／快照后先选A再预填，不沿用当前B资料或打开复审。improved显示“违规范围缩小，问题仍存在”，原fail及前后深链保持。

390／768／1440px按聊天→当前计划／进度→证据／报告的阅读顺序；窄屏分栏可折叠但不隐藏确认差异／缺项／取消。Tab焦点可见、表单有标签、状态有文字、长证据和ID可换行。图标用本地SVG，不能依赖缺字字体。原型浏览器检查仍只算对应模拟页面。正式Go／Vue已有人工输入下的对话、Guidance、业务草稿、可信归属与分批浏览器检查，范围见[当前记录](../validation/evidence/conversation-implementation-local.md)；真实云／模型、正式评价与完整M0尚未验收。
