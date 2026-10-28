# E0 实际接口与事件契约

本目录仅记录已实现的本机人工联调入口，不是正式审计、会话消息或报告 API。接口统一以 `/api/v1` 开头；所有路由均需 `X-App-Token` 请求头，应用口令不放在 URL 或接口示例中。

## 生成与留存

在仓库根目录运行：

```bash
bash scripts/e0.sh api
```

固定 `swaggo/swag v1.16.6` 从 [main.go](../../server/cmd/server/main.go)、[Gin handler 注释](../../server/internal/httpapi/api.go)和[实际 DTO](../../server/internal/storage/store.go)生成 [swagger.json](swagger.json)／[swagger.yaml](swagger.yaml)。生成器只需要公开依赖；不读取私有配置、不调用云或模型。产物随源码保留，不手工更改生成字段。实际路由与 Swagger 路径／方法由 Go 集成测试双向核对。

2026-10-28 首次实际生成：E0 health、人工任务创建／查询／取消、事件分页和 SSE；正式 `/execution-plans`、`/jobs`、消息、审阅、报告未实现，不放占位规范。API 文档目前通过仓库文件查看，没有额外文档 HTTP 服务或 Swagger UI。

## SSE 与轮询

[events.schema.json](events.schema.json)定义已实现的三类 E0 事件。完整业务事件设计仍以[前端事件设计](../design/frontend-and-events.md)为准；后续实现时扩展相应类型，不能把当前子集当成全部业务支持。

- `GET /api/v1/e0/jobs/{id}/events?after_seq=N&limit=100` 返回 `events/next_seq/has_more/last_seq`；页与高水位在同一短读事务内获取，返回前关闭事务和行游标。
- SSE 路径为上述路径追加 `/stream`，以带访问头的 `fetch` 读取。每帧 `id=seq`、`event=type`、`data=完整 JSON`；空行结束帧，15 秒注释心跳无 seq。数据提交后才能被读取和发送。
- `after_seq` 必须是非负十进制安全整数且不超过本任务尾部；越界／格式错误返回 422。页大小 1～100。
- 订阅寿命 120 秒，响应头 `X-Subscription-Lifetime-Seconds: 120`；每次写期限 10 秒，最大 8 个订阅。终态尾部排空后关闭；已终态且游标到尾部的订阅返回 204。
- 断开、切换 SSE／轮询只停止观察。显式取消是独立 POST；重连不会创建任务或增加本机执行次数。前端每任务持有独立的最后连续 seq；刷新后从 0 重新读取，不单独持久化游标。
- E0 人工任务没有模型消息或实际规则结果；上游模型探针仍为非流式 Chat Completions，浏览器 SSE 不代表模型流式。

测试与未验证边界见 [E0 执行记录](../validation/evidence/e0-local.md)。
