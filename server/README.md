# 只读采集、固定输入审计与受限Agent服务

三边界规则、会话／确认／计划／审阅／报告和[五视图Web](../web/README.md)已接通；E3新增正式Eino单任务Runner、九个固定资料工具、有限上下文、持久Agent结果和共享物理HTTP账本。E3限定离线验证已通过，历史结果见[E3记录](../docs/validation/evidence/e3-local.md)；E4已接显式云配置、固定只读SDK适配／采集、共享预算和云产品结果，已通过的限定人工协议验证见[E4适配记录](../docs/validation/evidence/e4-adapters-local.md)，前序见[E1](../docs/validation/evidence/e1-local.md)／[E2](../docs/validation/evidence/e2-local.md)。

默认不配置模型即可运行离线规则；正式服务不读取dotenv、默认profile或独立实验私配。未配置模型时，rules-only可执行；rules-and-agent保留规则结果并明确模型阶段未执行／partial，助手发送返回受控未配置错误。显式启用云／模型后，仅已接受计划或明确发送产生有界调用，启动／配置检查／预览零外呼。E0人工HTTP诊断和[独立协议探针](internal/protocolprobe/README.md)继续隔离。

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

迁移是明确的独立操作，不在正常启动时AutoMigrate。当前要求`business_schema=4`；旧库须先停止服务并备份，再显式运行`migrate`。迁移保留已有业务／账本／Agent与诊断记录，增加配置范围目录和云产品结果／私有原文。普通打开拒绝旧版或缺必要表／列的非完整schema 4，不在启动时修库。默认库为`data/private/e0.sqlite`，只监听`127.0.0.1:8080`；缺应用口令拒启。该口令只保护本机应用访问，不使用云／模型密钥。

开发时另一个终端按[Web说明](../web/README.md)启动`127.0.0.1:5173`并打开`/overview`。浏览器只在内存保存访问口令，经开发代理调用API。

已有构建也可由同一Go服务提供静态文件：先在`web/`运行`npm run build`，再从`server/`运行`go run -mod=readonly ./cmd/server --web-dir ../web/dist`。这是实际可选参数；业务深链接与静态资源按明确路由提供，未知API不会退回SPA页面。`npm run preview`仍只是静态预览，不代理API。当前仅支持本机开发，公网部署、TLS及生产身份体系不在本轮。

## 显式模型配置与本地检查

空白结构见[model-config.example.json](model-config.example.json)，默认disabled，端点／模型及预算周期均为占位示意。复制到私有忽略目录后按已有模型与授权范围填写，不把模板次数或远期到期值当已获额度，也不替换原MiniMax或提高512等更严限制。

从`server/`运行实际参数；下列路径指操作者自行准备、放在忽略目录的私有JSON，命令读取并在本地校验，只回显脱敏摘要：

```bash
go run -mod=readonly ./cmd/server --model-config ../data/private/model-config.json --config-check
```

配置使用`api_style=chat_completions`及显式`provider_profile`（`minimax`或`openai-compatible`）、HTTPS `base_url`、`model`、`api_key_env`、`enabled`、`max_output_tokens`、`timeout_seconds`、`max_http_attempts`和`budget`。只读取`api_key_env`点名的进程变量；disabled不要求读取密钥。budget显式包含`ref`、`expires_at`、应用／会话模型尝试上限；同一周期只允许收紧，换密钥、重启或新建会话不重置已用次数。

`--config-check`不打开业务库、不启动服务或工作器、不做DNS／HTTP，只输出configured、enabled、协议／provider类型和限额等脱敏摘要，不回显端点、模型名、环境变量名、预算引用或密钥。configured／enabled只说明本地配置状态，不等于认证、上游兼容或付费授权。保留既有MiniMax模型及512等更严格私配，配置与项目上限取严，不在失败时换模型或抬额。

确需运行模型时，操作者先明确数据／权限／预算，再用同一`--model-config`启动服务。初始化与计划预览仍零外呼；审计须接受计划，助手须明确同意后发送。当前测试使用in-memory或loopback人工脚本模型协议，未证明真实MiniMax连续兼容或自主选择质量。

## 显式云配置与当前采集切片

空白结构见[cloud-config.example.json](cloud-config.example.json)，默认disabled，账号／资源／预算均为占位示意。只支持env_ak或env_sts，从同一JSON点名的环境变量取得静态AK或STS三元组；不使用默认profile／dotenv／实例元数据，不实现AssumeRole或ECS角色自动续期。STS可记录已知到期，未知到期不伪称已知或可刷新。生产CFW／ECS／STS端点由受控映射固定，不接受前端或配置任意URL。

从server目录进行纯本地检查；可同时传--model-config，摘要不回显密钥、真实账号／资源或端点：

```bash
go run -mod=readonly ./cmd/server --cloud-config ../data/private/cloud-config.json --config-check
```

启动时只注册本地配置范围，GET /api/v1/data-sources/{id}/collection-options也只返回本地允许scope／资产和可用性，identity_verified保持未核验，不触发身份或产品请求。实际执行前须明确授权账号、地域、资产／单NAT网关、API和预算，再预览并接受collect或connection_check计划；身份先以GetCallerIdentity核对expected_account_id和受支持身份／ARN，再允许固定CFW五操作与ECS两操作。

当前collect只支持明确的assets（最多50个允许ID）或NAT gateway_policy，不支持discover_region、空列表全量发现或跨账号。最多两轮配置族读取，页数／总数／重复／引用与未知条件保留完整性和一致性限制；默认动作保持unknown，不以同VPC证明NAT关联。安全组只作关联后只读上下文，不证明监听、角色或业务授权。collect完成先原子保存新snapshot／sealed输入和cloud_result，用户随后另行预览并接受pinned audit；当前fresh audit通过这两步衔接，不隐式自动审计。

connection_check当前只做身份检查，不读取产品配置，结果为identity_verified=true、product_ready=false、IDENTITY_ONLY局限与partial／CLOUD_LIMITED；它不证明CFW／ECS产品权限、资源可见性、字段或分页完整。两份云／模型配置同时启用须同budget.ref，应用／会话额度及到期取严；云配置显式模型0上限保持0，不因加云适配而启用模型或重置周期。

所有云／凭据／模型物理HTTP共用一个Manager与持久预记。原始产品响应按原始bytes计算SHA-256后以base64存私有列，保留精确字节；不把原文、凭据、私有配置修订或过滤器放cloud_result／事件／报告。云结果只投影身份／产品阶段、观察窗口、页数、完整性与受控局限；取消或重启不自动重放。

当前E4测试使用真实SDK／HTTP／账本／Collector／数据库连接人工loopback响应，所产live_readonly仅标识受控采集代码路径，不是用户账号真实云观测。实际身份／权限、字段／分页、上游MiniMax连续兼容、真实整改复审与E5正式评价仍未验收；实际结果与局限见E4适配记录。

## 操作与持久化边界

- 正式API使用`/api/v1/jobs`，E0诊断使用`/api/v1/e0/jobs`；列表和单任务读取／事件／取消按kind隔离。正式启动只消费已保存`plan_id`，不能在启动时覆盖范围或效果上限。
- 本地导入最大5 MiB，来源由服务器标为`imported`，不能自称live或改变采用基线。五视图读取固定快照、原始规则及其冻结证据，不以最新业务版本覆盖旧结果。
- 角色和访问分开确认；草稿→差异预览→明确确认／拒绝，共用修订条件和幂等。业务例外绑定资产／服务、具体检查、理由和到期时间；不改原fail或严重程度。
- 同进程3个任务槽位、最多10个等待任务，同会话1个未结束任务。正式队列有持久期限，排队超时零外呼；取消独立于观察断开。重启保留有效正式queued，运行中任务中断且不自动重放；E0遗留queued继续按诊断语义中断。
- 复审`POST /api/v1/audits/{job_id}/rechecks`只生成独立预览，接受后仍用`POST /api/v1/jobs`幂等消费。旧对象在新快照缺失时返回409 `RECHECK_NOT_COMPARABLE`／`object_missing`，不缩减范围、不改旧报告。已保存比较由`GET /api/v1/jobs/{id}/result`的`recheck`读取。
- 任务、消息、规则结果／证据、审阅和固定报告持久化。规则先保存；正常报告、公开结果消息和最终状态在短事务中发布。保存失败不虚报成功，同键重试返回已提交对象。
- 每任务新建Eino ChatModelAgent／Runner和工具闭包；同批call ID先整体校验，新返回引用到下一轮才晋升可用。九个工具只读封存输入／原规则结果，按范围、来源、时点、字段与字节限制投影；安全组未采集明确返回缺口，不隐式补云调用。
- ContextBuilder最多带入20条／24 KiB获准历史，固定接受消息位置和原计划范围，排除跨范围、旧工具wire及reasoning，保存裁剪／排除计数。完整协议消息仅存当前执行内存；产品结果保存前重验固定输入、版本和本地证据绑定。
- 所有模型物理HTTP尝试经共享准入，在短事务预记后才可发出；应用／会话／任务上限取严。失败、未知送达、预记后未发保守计数，不自动退款或补发。逻辑模型／工具次数与物理尝试分开；缺usage保持unknown，显式零与部分报告保留，聚合溢出使reported_tokens为空并标overflow。
- `GET /api/v1/jobs/{id}/result`包含`snapshot_id`、`agent_result`、`attempt_usage`和`cloud_result`；`GET /api/v1/jobs/{id}/input`返回该任务sealed固定输入。模型阶段与job终态分别记录，模型已完成后任务仍可能取消／中断。受保护任务结果保留完整经校验解释；公开JSON／Markdown的Agent层只用固定模板、公共目标／证据别名和计数，不复制模型自由文案、不覆盖原规则判定。
- 事件与状态同事务提交，fetch SSE／轮询只读同一事件表。重连只补观察，不创建／取消任务、不恢复旧执行或重置预算；八类事件和受控载荷见[实际接口契约](../docs/api/README.md)。

存储使用GORM＋libtnb/sqlite／modernc，普通共享池最大8连接、空闲4；每连接WAL、外键、5秒busy timeout，短事务不跨网络。使用驱动的`immediate`普通事务，没有写队列、读写分池或checkpoint调度。

Linux／WSL先取得数据目录独占锁，再恢复持久状态；第二进程不得恢复首进程任务。备份先停止进程并关闭库，再复制数据库到独立私有目录，不只拷运行中主文件。私有库、原始资料、备份、未脱敏报告、构建产物和依赖缓存放忽略目录。按Ctrl+C停止；上游模型／真实云、20～30正式评价案例、2 CPU／4 GiB标准性能及真实人工整改复审仍须分别取证，E4可离线适配继续按计划推进。
