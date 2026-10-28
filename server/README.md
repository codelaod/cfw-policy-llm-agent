# 正式离线审计服务

E2已把三边界规则核心接入会话、公开消息、业务确认、执行计划、任务、审阅、固定报告和复审API，并与[五视图Web](../web/README.md)联动。当前阶段验证边界见[E2记录](../docs/validation/evidence/e2-local.md)，前序规则证据见[E1记录](../docs/validation/evidence/e1-local.md)。

正式离线API不调用真实云或模型，不读取`.env.local`、云／模型凭据或机器profile。未配置模型时，rules-only可执行；rules-and-agent保留规则结果并明确模型阶段未执行／partial，助手发送返回受控未配置错误。E0仅保留独立本机人工HTTP诊断，不产生业务发现。Eino／云SDK协议探针仍在[独立包](internal/protocolprobe/README.md)，不等于正式Agent或真实接入验收。

## 构建与启动

在仓库根目录操作，Go 1.26.x；阶段环境版本见执行记录。依赖及校验值固定在[go.mod](go.mod)／[go.sum](go.sum)，独立Eino实验保持原模块。现有脚本保留`e0.sh`名称，实际已覆盖本模块的当前测试、构建、迁移及接口生成。

```bash
bash scripts/e0.sh test
bash scripts/e0.sh build
bash scripts/e0.sh migrate
read -rs -p '输入至少16字符的本机应用口令：' CFW_APP_TOKEN
export CFW_APP_TOKEN
bash scripts/e0.sh server
```

迁移是明确的独立操作，不在正常启动时AutoMigrate。E2要求`business_schema=2`；旧E0／E1库须先停止服务并备份，再显式运行`migrate`。迁移保留既有快照、结果和诊断历史，补会话／确认／计划／任务关系及审计冻结输入；普通打开拒绝未迁移库。默认库为`data/private/e0.sqlite`，只监听`127.0.0.1:8080`；缺应用口令拒启。该口令只保护本机应用访问，不使用云／模型密钥。

开发时另一个终端按[Web说明](../web/README.md)启动`127.0.0.1:5173`并打开`/overview`。浏览器只在内存保存访问口令，经开发代理调用API。

已有构建也可由同一Go服务提供静态文件：先在`web/`运行`npm run build`，再从`server/`运行`go run -mod=readonly ./cmd/server --web-dir ../web/dist`。这是实际可选参数；业务深链接与静态资源按明确路由提供，未知API不会退回SPA页面。`npm run preview`仍只是静态预览，不代理API。当前仅支持本机开发，公网部署、TLS及生产身份体系不在本轮。

## 操作与持久化边界

- 正式API使用`/api/v1/jobs`，E0诊断使用`/api/v1/e0/jobs`；列表和单任务读取／事件／取消按kind隔离。正式启动只消费已保存`plan_id`，不能在启动时覆盖范围或效果上限。
- 本地导入最大5 MiB，来源由服务器标为`imported`，不能自称live或改变采用基线。五视图读取固定快照、原始规则及其冻结证据，不以最新业务版本覆盖旧结果。
- 角色和访问分开确认；草稿→差异预览→明确确认／拒绝，共用修订条件和幂等。业务例外绑定资产／服务、具体检查、理由和到期时间；不改原fail或严重程度。
- 同进程3个任务槽位、最多10个等待任务，同会话1个未结束任务。正式队列有持久期限，排队超时零外呼；取消独立于观察断开。重启保留有效正式queued，运行中任务中断且不自动重放；E0遗留queued继续按诊断语义中断。
- 复审`POST /api/v1/audits/{job_id}/rechecks`只生成独立预览，接受后仍用`POST /api/v1/jobs`幂等消费。旧对象在新快照缺失时返回409 `RECHECK_NOT_COMPARABLE`／`object_missing`，不缩减范围、不改旧报告。已保存比较由`GET /api/v1/jobs/{id}/result`的`recheck`读取。
- 任务、消息、规则结果／证据、审阅和固定报告持久化。规则先保存；正常报告、公开结果消息和最终状态在短事务中发布。保存失败不虚报成功，同键重试返回已提交对象。
- 事件与状态同事务提交，fetch SSE／轮询只读同一事件表。重连只补观察，不创建／取消任务、不恢复旧执行或重置预算；八类事件和受控载荷见[实际接口契约](../docs/api/README.md)。

存储使用GORM＋libtnb/sqlite／modernc，普通共享池最大8连接、空闲4；每连接WAL、外键、5秒busy timeout，短事务不跨网络。使用驱动的`immediate`普通事务，没有写队列、读写分池或checkpoint调度。

Linux／WSL先取得数据目录独占锁，再恢复持久状态；第二进程不得恢复首进程任务。备份先停止进程并关闭库，再复制数据库到独立私有目录，不只拷运行中主文件。私有库、原始资料、备份、未脱敏报告、构建产物和依赖缓存放忽略目录。按Ctrl+C停止；真实云／模型、正式Agent、标准性能和真实人工整改验收仍须分别取证。
