# 正式Go服务

Go／Gin／Eino单进程，纯Go SQLite WAL与普通连接池；Vue／Vite构建可由同服务提供。默认可使用人工／导入资料与确定性规则。当前客户主链整改和独立Review修复已通过限定本机复验；完整24旅程／88分支及真实接入仍待验收，阶段证据见[代码记录](../docs/validation/evidence/conversation-implementation-local.md)，剩余项见[实施计划§2.3](../docs/development/implementation-plan.md)。

## 运行与显式迁移

工具链：`go.mod`要求Go 1.26，本次验证使用Go 1.26.10（Linux／WSL、CGO_ENABLED=0）。

仓库根目录：

```bash
bash scripts/e0.sh test
bash scripts/e0.sh build
bash scripts/e0.sh migrate
bash scripts/e0.sh server
```

当前business schema为5。旧库先停止服务并备份，再显式migrate；正常启动不AutoMigrate、不修复缺表／必要列／授权head的非完整库。迁移保留旧业务／attempt／Agent／云结果，新增目录、目标／提议、授权修订与准备绑定等结构。默认私库data/private/e0.sqlite；监听127.0.0.1:8080。设置CFW_APP_TOKEN为至少16字符的本机应用口令，不能拿云／模型密钥代替。

脚本server／migrate透传参数；--env-file／--model-config／--cloud-config／--execution-config／--budget-change／--db／--web-dir的显式相对路径以脚本调用者目录解析，支持等号和空格。直接go run的路径以其工作目录解析。下例从server目录运行：

```bash
go run -mod=readonly ./cmd/server --web-dir ../web/dist
```

先在web运行npm run build。静态业务深链接与未知API分开，npm run preview不代理API。当前为本机开发交付；公网发布、生产TLS／身份体系另按范围安排。

## 模型来源与配置检查

正式模型二选一：

- --env-file：显式读取原项目七字段CFW_MODEL_BASE_URL、CFW_MODEL_API_KEY、CFW_MODEL_NAME、CFW_MODEL_API_STYLE、CFW_MODEL_TIMEOUT_SECONDS、CFW_MODEL_MAX_CALLS、CFW_MODEL_MAX_OUTPUT_TOKENS；进程LookupEnv存在即优先，显式空值也覆盖文件。只作字面解析，不执行shell，不自动搜索.env.local或其他dotenv。
- --model-config：显式私有JSON，凭据只从其中点名的环境变量读取。与--env-file同时出现拒绝，不静默选一方。

原模型名称与512等更严私限保留；兼容来源不会替换MiniMax模型。JSON字段冲突、重复／大小写碰撞、未知模型字段和结构错误拒绝；缺凭据、disabled、过期、无授权等与结构错误分开。空白示例见[模型JSON](model-config.example.json)和[原环境字段示例](../.env.example)。运行配置检查不回显密钥、私有URL／账号／资源或凭据片段：

```bash
go run -mod=readonly ./cmd/server --env-file ../.env.local --execution-config ../data/private/execution.json --config-check
go run -mod=readonly ./cmd/server --model-config ../data/private/model-config.json --config-check
```

enabled／configured、local ready、持久账本状态、上游兼容已验证与本次任务效果不是同一件事。配置检查不探测上游；已启用但未上游验证时，可以在已接受权限／预算内尝试，不把未验证一律禁用。模型不存在时规则审计仍可用，选择模型补充的审计保留规则与明确partial。

## 云连接、目录与本次对象

[云配置示例](cloud-config.example.json)默认disabled。只支持env_ak／env_sts静态Provider，从JSON点名环境取AK或STS三元组；不读取机器profile／实例元数据，不做AssumeRole／自动续期。CFW／ECS／STS生产端点由受控映射固定，账号先按expected_account_id与受支持身份核对。

连接提供单账号、有限地域与边界的读取上界。allowed_assets／allowed_nat_gateways可选作额外收紧，缺省不要求逐台登记。客户从对话提供名称／ID／类型／地域／有限标签或确认归属，目录负责定位，本次计划负责决定审计对象。身份检查与目录展示不会替代产品权限／完整性证明。

接受有限资料准备计划后，同一个audit任务可先核缓存资格、必要时做有限metadata查询／采集，再一次封存规则与Agent输入。多个同名对象、未穷尽或未知筛选条件时停在澄清；不选首条、不扩账号／地域。ECS批量分页，SG按轮去重并保留关联和原条件。NAT仅CFW管理目录、精确匹配原生／防火墙名称，一个任务绑定一个网关，不宣称VPC全网关或完整转换审计。

首轮非fatal失败在所选真实身份齐备时保存明确partial输入及已得ACL／SG；不足、fatal或取消只保存安全观察和受控原因，无snapshot／成功审计。取消已提交的迟到成功不越过发布准入；敏感反射不写收据。

独立collect／pinned固定快照入口保留。connection_check可用identity_only且不要求资产，接受后仅身份读取，结果仍IDENTITY_ONLY partial，不读取产品配置，不证明CFW／ECS字段与权限。云写工具不存在，用户人工应用策略。

## 执行保护、时效与持久授权

[execution-config.example.json](execution-config.example.json)为公开结构示例，预算全为0，不产生调用授权。显式--execution-config选择四类完整workload：排队／执行、采集／规则／模型／收尾、单模型请求、逻辑模型次数、物理尝试、工具／批量和对象／计算保护。已有私配取严；显式0不回填。未提供时使用有明确legacy标签的历史兼容值，均非厂商SLA或永久产品上限；真实等待和用量须另校准。

可选data_freshness包含version、configuration_max_age_seconds、metadata_max_age_seconds。资格还核来源、配置修订、原窗口、双轮稳定、完整性、引用闭合与所需关联，不是单看TTL。未配置使用标注未校准的兼容政策。复用保持原时点；force_refresh／人工变更时点不偷用旧资料，历史解释零云。

云和模型共用一个Manager、application-root持久授权与attempt账本。Ref不能被轮换以重置累计；同Ref追加修订使用base revision CAS、change_id幂等、actor／reason／authorization来源。可明确无到期，或有到期取严；适配器私限不会因共享授权扩大自动放宽。普通启动只建初版或收紧，不自行追加额度／续期。已绑定授权可由env模型继续用，未绑定不凭配置发明额度。

本地--budget-change读取显式修订JSON，应用已有账本后退出，与启动／migrate／config-check互斥；它是独立管理员操作，需停止持有数据库锁的服务。未知送达仍计已用，修订不删attempt。启动、config-check、计划预览与Guidance均零外呼；实际请求只在已接受任务内，逐物理尝试（含重试）预记后发送。

## 对话、业务与结果

无快照可建会话并保存目标；候选／澄清／提议／接受持久，旧目标变化使旧计划失效。固定历史的解释沿原报告时点，模型历史引导先绑定原报告／快照并预填，发送仅生成零云解释计划，再接受才允许模型；人工改后强刷新审计并关联旧报告，不回写旧结论。业务草稿／对话／表单共用，角色、允许、必要访问与例外分别预览确认；可信归属绑定有限成员及版本，可跨页取齐。自然确认绑定已核对的目标版本，原计划消费、用户消息和回执同事务保存；旧请求重放不回退新目标。

超过单批时保留完整有限集合，明确预览后由一个父任务串行推进内部批段，共用原总期限、阶段和调用额度，不按批复制授权。每批有独立输入、时点、审计及报告；父结果只列实际覆盖、失败和未处理，不造合并报告。资料导入受5MiB／10000资产等结构保护，与单批执行上限分开，原始来源不变。

同稿业务冲突通过只读current-base核对当前值，明确选择后以revision＋heads双CAS rebase；原稿ID和对话绑定保留，旧preview作废、不确认业务，原提交标识可恢复。新增rebase回执属schema5必要结构，旧库须显式迁移，正常启动不补表。

单进程3个不同会话并行，同会话一个未结束任务，第四个排队。SSE＋轮询只观察持久seq，提交后发送、断线不取消、重连不重放请求或返还额度。服务重启把旧运行标中断，不自动恢复云／模型调用。事务不跨网络等待。

固定九工具只读sealed输入／原规则结果，metadata／SG有来源、时点、别名与缺口；角色和端口不证明监听、可达或业务授权。最多20条／24KiB获准公开历史，排除跨范围和未解析目标池，不保存原始reasoning或孤立旧工具wire。原始规则结果不被模型覆盖，报告JSON／Markdown使用受控公共投影及实际workload，保留unknown usage与失败阶段。

GET /api/v1/jobs/{id}/result分别提供准备观察、CloudRecord、规则／Agent、用量和最终性；sealed输入用受保护input接口查看。共用版本化GET /guidance仅返回本地下一步，短语预填／本地导航不自动执行任务。实际接口实现后用固定Swag生成[Swagger JSON／YAML](../docs/api/README.md)。

## 存储与证据边界

GORM＋libtnb/sqlite／modernc、无CGO；普通共享池最多8连接／空闲4，每连接WAL、外键与5秒busy timeout。Linux／WSL先取得私库目录独占锁，第二进程不得恢复首进程任务。备份先停止进程并关闭库，再复制到独立私有目录；不只拷运行中主文件。不自建写队列、读写分池或checkpoint平台。

固定模型对照及三组执行／独立评分入口见[evaluation](internal/evaluation/README.md)；只消费调用者已接受并claim的普通任务，不新增公开产品接口、不创造grant、不自动运行正式评价。

私配、原始资料、私库、备份和未脱敏报告放忽略路径。当前签名SDK／loopback／人工Eino模型及浏览器证据不是真实账号或真实模型质量；真实身份／权限／字段、MiniMax连续兼容、正式三组评价、2CPU／4GiB性能与至少一次真实人工整改复审仍须各自取证。
