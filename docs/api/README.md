# 实际业务接口与事件契约

本目录记录已注册的正式离线业务API及隔离的E0人工诊断接口。统一前缀`/api/v1`；数据和任务路由均需`X-App-Token`请求头，应用口令不放URL、业务记录或接口示例。默认离线规则不读云／模型凭据；E3模型只由服务端显式私有JSON配置注入。启动／配置检查／预览零外呼，接受计划或明确发送后才有受预算约束的请求；模型未配置明确反馈，采集／真实云接入尚未实现，不以人工协议fixture冒充真实接入。

## 生成与留存

在仓库根目录运行：

```bash
bash scripts/e0.sh api
```

固定`swaggo/swag v1.16.6`从[main.go](../../server/cmd/server/main.go)、[基础handler](../../server/internal/httpapi/api.go)、[业务handler](../../server/internal/httpapi/workspace.go)和实际storage／app／domain／agentquery DTO生成[swagger.json](swagger.json)／[swagger.yaml](swagger.yaml)。生成器只读取公开源码／依赖，不读取私有配置、不调用云或模型。产物随源码留存，字段由注释／DTO生成；Gin实际路由与Swagger路径／方法通过集成测试双向核对。E3已实际再生成并通过TestGeneratedSwaggerMatchesActualRoutes双向对账：41条路径、49项操作；结果与边界见[E3记录](../validation/evidence/e3-local.md)，E2历史统计保持原证据。没有额外Swagger UI或文档HTTP服务。

[server/.swaggo](../../server/.swaggo)将`json.RawMessage`替换为`interface{}`，因为这些字段在实际响应中保存原始JSON值，不是base64字节数组。override只修正生成器的类型表达，不转换运行时数据，也不把任意输入视为已验证业务对象；具体字段仍按对应DTO／服务校验。

已注册范围包括本地能力／来源／范围、当前工作区、会话和公开消息、人工样例／快照导入、资产与策略、业务草稿与分部确认、执行计划与任务、冻结发现／证据与审阅、固定报告和复审预览。E3已接正式Eino工具链、持久Agent结果及封存任务输入读取；尚未接入的云连接、刷新／采集或其他路由不放占位规范。

## 关键操作语义

- `POST /execution-plans`只保存固定离线输入与效果预览；`POST /jobs`只接收`plan_id`，用`Idempotency-Key`消费。不能在启动时换范围、模式或预算。列表默认100条，最多100条；偏移量及游标由服务端校验。普通请求正文上限32 KiB，快照导入上限5 MiB。
- 消息发送以`client_message_id`幂等，同意模型使用不等于自动刷新云。已接受请求可先只读回放原消息和任务；当前模型／范围／归档状态变化不会新建请求。异内容同键返回409。
- 草稿确认必须带预览标识、草稿修订及明确选择部分；roles与access独立保存版本。角色确认不会暗中确认访问或业务例外。
- `POST /audits/{job_id}/rechecks`只返回新计划，仍须显式`POST /jobs`消费；缺旧对象返回409 `RECHECK_NOT_COMPARABLE`，受控原因`object_missing`。成功比较通过`GET /jobs/{id}/result`的`recheck`读取，无已保存比较时为null；旧报告不变。
- 发现详情的证据值来自该审计保存的冻结输入。审阅只追加意见或todo／deferred计划；报告固定生成时的审阅头。公开内容按报告内别名投影，下载仍用带访问头的请求。
- `GET /jobs/{id}/result`在同读快照返回`snapshot_id`、`agent_result`、`attempt_usage`和原始规则；`GET /jobs/{id}/input`只读该任务sealed固定输入。证据展开使用本任务绑定，不以当前快照／业务头替代。物理尝试、known／unknown usage及聚合overflow独立于逻辑模型／工具次数，空token不当零费用。
- 受保护任务结果保留完整经校验解释；公开JSON／Markdown的Agent层只用固定模板与公共别名，不复制模型自由文案或本地私有映射。模型阶段completed与job终态分别记录，取消／中断不因模型已完成而改写为任务成功。
- 正式`/jobs`与`/e0/jobs`按kind隔离，不能串用单任务／事件／取消端点。E0人工任务没有业务规则发现或模型消息。

## SSE 与轮询

[events.schema.json](events.schema.json)列出当前协议支持的八类事件：`job_status`、`stage_changed`、`tool_started`、`tool_finished`、`findings_available`、`message_available`、`report_available`、`job_terminal`。E3正式Runner实际工具回调经app sink保存`tool_started`／`tool_finished`，载荷为`phase=L3`、`tool`、`call_id`、受控`status`、`elapsed_ms`和`evidence_refs`（无引用的结束载荷可为null）；不使用旧草案tool_name，不伪造未执行工具事件。`stage_changed`在Agent结果提交后可带`agent_result_ref`。本地人工脚本协议不证明真实模型自主选择或上游兼容。

- `GET /api/v1/jobs/{id}/events?after_seq=N&limit=100`返回`events/next_seq/has_more/last_seq`；E0路径为`/api/v1/e0/jobs/{id}/events`。页和高水位在同一短读事务获取，返回前关闭事务和行游标。
- SSE路径追加`/stream`，以带访问头的fetch读取。每帧`id=seq`、`event=type`、`data=完整JSON`，空行结束；15秒注释心跳没有seq。事件与业务状态提交后才能被读取和发送。
- `after_seq`必须是非负十进制安全整数且不超过本任务尾部；格式／越界返回422。页大小1～100，完整事件UTF-8正文不超过4 KiB。载荷只传受控阶段／错误码、摘要和持久对象引用，不传凭据、完整工具原文或内部reasoning。
- 订阅寿命120秒，响应头`X-Subscription-Lifetime-Seconds: 120`；每次写期限10秒，最大8个订阅。终态包括partial，尾部排空后关闭；已终态且游标到尾部返回204。
- 断开及SSE／轮询切换只停止观察，取消是独立POST。重连不创建任务、不重放云／模型调用、不重置共享额度。前端按任务保存最后连续seq；刷新后重新读取完整持久视图，不孤立恢复游标。
- 公开助手消息整体保存后再发`message_available`；上游首适配仍为非流式Chat Completions，浏览器SSE不代表上游模型token流。

当前实现与已通过的限定离线验证见[E3记录](../validation/evidence/e3-local.md)；前序[E2](../validation/evidence/e2-local.md)／[E0](../validation/evidence/e0-local.md)证据保持各自历史边界。
