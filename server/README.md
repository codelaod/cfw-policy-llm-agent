# E0 本机后端骨架

这是 Go／Gin／纯 Go SQLite 的依赖与契约验证入口；Eino 和云 SDK 的本机协议探针在 [internal/protocolprobe](internal/protocolprobe/README.md)。任务只发送进程内人工 HTTP 请求，不读取云／模型配置。正式业务会话、规则、证据、审阅和报告留到 E1～E3。

## 构建与启动

在仓库根目录操作，Go 1.26.x；本次实测版本为 1.26.10。依赖及校验值固定在 [go.mod](go.mod)／[go.sum](go.sum)，独立 Eino 实验保持原模块。

```bash
bash scripts/e0.sh test
bash scripts/e0.sh build
bash scripts/e0.sh migrate
read -rs -p '输入至少16字符的本机应用口令：' CFW_APP_TOKEN
export CFW_APP_TOKEN
bash scripts/e0.sh server
```

迁移是明确的独立操作，不在正常启动时 AutoMigrate。默认库为 `data/private/e0.sqlite`，只监听 `127.0.0.1:8080`；缺口令拒绝启动。输入口令只供本地访问保护，与云／模型凭据无关，不要将真实云／模型密钥用作此值。

另一个终端按 [Web 说明](../web/README.md)启动 `127.0.0.1:5173/e0`。浏览器输入同一应用口令，当前页面仅在内存保存。开发代理支持同源 API；本阶段后端不托管静态产物，`npm run preview` 只用于静态预览，不是完整应用部署入口。

## 已实现边界

- `/api/v1/e0/*` 只接受有界人工会话／幂等标识；没有接收任意 URL、云参数或模型参数的入口。接口和状态见[生成规范](../docs/api/README.md)。
- 单进程三槽位、最多十个待执行任务、同会话一个未结束任务。每任务最多一次本机 HTTP 请求，不重试。取消独立于观察断开。
- GORM＋libtnb/sqlite／modernc，普通共享池最大 8 连接、空闲 4；每连接 WAL、外键、5 秒 busy timeout，短事务，不跨网络持有连接。使用驱动的 `immediate` 普通事务，未增加写队列、读写分池或 checkpoint 调度。
- Linux／WSL 文件锁在读取／恢复前取得；退出或重启将 E0 遗留任务标记中断，取消中的任务保留取消语义，不重放人工 HTTP。E0 的 queued 也中断；正式任务的队列期限、预算与恢复策略在 E2 实现。
- 事件与状态一并持久化，SSE／轮询只读同一事件表，尾部补发后结束。人工工作不产生正式规则发现或模型消息。
- 停机备份需先停止进程并关闭库，再复制数据库到独立私有目录；不要只复制运行中主文件。已有测试实际覆盖停机复制／独立目录恢复；引用原文等业务数据尚未实现。

按 Ctrl+C 停止。私有数据库、构建产物、依赖缓存放在忽略目录。当前只支持本机开发；未实现公网部署、完整业务持久化或正式生产认证。
