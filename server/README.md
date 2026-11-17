# 正式Go服务

Go／Gin／Eino单进程，纯Go SQLite WAL与普通连接池；Vue／Vite构建可由同服务提供。默认可使用人工／导入资料与确定性规则。当前客户主链整改和独立Review修复已通过限定本机复验；完整24旅程／88分支及真实接入仍待验收，阶段证据见[代码记录](../docs/validation/evidence/conversation-implementation-local.md)，剩余项见[实施计划§2.3](../docs/development/implementation-plan.md)。

## 统一配置与运行

所有外部设置只走一份[配置模板](../.env.example)：应用口令／数据库／静态目录、模型、阿里云连接、共享预算、数据时效及四类任务执行保护。使用同名进程环境变量时无需配置文件；使用文件时由`--env-file`显式选择，不自动查找dotenv、机器profile或实验私配。旧模型／云／执行JSON入口及对应模板已删除。

从仓库根目录准备本地文件：

```bash
cp -n .env.example .env.local
chmod 600 .env.local
bash scripts/e0.sh server --env-file .env.local --config-check
bash scripts/e0.sh migrate --env-file .env.local
bash scripts/e0.sh server --env-file .env.local
```

已有`.env.local`直接编辑即可。先在web目录构建`npm run build`，或把`CFW_WEB_DIR`留空并使用Vite开发代理。监听固定为127.0.0.1:8080；浏览器输入应用口令，模型和云凭据只留服务端。当前是本机开发交付；公网发布、生产TLS／身份体系另按范围安排。

| 配置组 | 填写位置／形式 |
| --- | --- |
| 应用 | `CFW_APP_TOKEN`至少16字符；`CFW_DB_PATH`为私库路径；`CFW_WEB_DIR`为空或构建目录 |
| 模型 | `CFW_MODEL_ENABLED`、`BASE_URL`、`API_KEY`、`NAME`、`API_STYLE`、`PROVIDER_PROFILE`、`TIMEOUT_SECONDS`、`MAX_HTTP_ATTEMPTS`、`MAX_OUTPUT_TOKENS`（后八项均加`CFW_MODEL_`前缀） |
| 阿里云 | `CFW_CLOUD_*`选择账号、地域、边界、静态凭据模式和限制；`CFW_ALIYUN_*`填写密钥整组。地域／边界／可选对象使用模板中的JSON数组 |
| 共享预算 | `CFW_BUDGET_REF`、可选到期、应用／会话云和模型累计物理尝试；没有ref且次数全0表示未配置，明确0禁止请求 |
| 数据时效 | `CFW_FRESHNESS_*`版本及配置／元数据最大复用时长，0不复用；还检查来源、完整性及引用等资格 |
| 执行保护 | `CFW_WORKLOAD_PROFILE_REF`；`CFW_WORKLOAD_<AUDIT/ASSISTANT_TURN/COLLECT/CONNECTION_CHECK>_<字段>`，完整字段及默认值均在同一模板 |

只有`--env-file`是配置来源参数。`--migrate`、`--config-check`和`--budget-change`是互斥操作；`--budget-change`仍使用一份明确的管理员变更JSON追加持久授权修订，它是操作请求，不是第二套启动配置。脚本也可运行`test`、`build`、`api`。

### 来源、优先级与检查

**进程中明确存在的值（包括空值） > 所选文件 > 程序默认值。** 数字和布尔值显式空会报错，0不会回填默认。云AK／secret／STS token是一个原子记录：进程中任一凭据变量存在，就整组从进程取值，缺项阻断该适配器，不能从文件补成混合身份。只读取项目明确字段，不回退到其他云／模型账号变量。

文件最大64KiB且必须是普通文件；支持BOM、CRLF、export、单／双引号和注释，按字面解析，不执行shell／变量展开。未知字段、大小写错误、重复键、引号错误、非法类型和控制字符拒绝；选定文件不存在也不回退。`.env.local`不进Git、不应source。配置对象的JSON／字符串格式化和`--config-check`均只输出脱敏摘要，不含密钥、地址、模型名、账号、路径或资源。

文件内相对路径以该文件目录解析。直接运行Go或二进制时，进程相对路径以运行目录解析，默认私库为`../data/private/e0.sqlite`（从server目录运行）；`e0.sh`先将显式文件参数及进程的DB／Web相对路径按脚本调用者目录转绝对路径，再切入server。等号、空格路径和空值均保留。

启动、配置检查及计划预览零DNS／HTTP；配置检查不开数据库、不启动工作器。摘要区分本地结构、凭据、授权、账本未检查和上游未验证；填写／enabled／ready不代表身份与真实协议已验证，也不触发调用。模型和云均可禁用并逐项填写，离线规则和历史可用。启用后缺密钥保持blocked；缺URL／模型名／期望账号属配置错误。provider profile只有openai-compatible／minimax；实际可执行非流式chat_completions，已知responses登记为不支持，不静默降级。

当前business schema为5。旧库先停止服务并备份，再显式migrate；正常启动不AutoMigrate、不修复非完整库。纯Go／CGO_ENABLED=0，Go版本要求见go.mod；本次本机验证工具链见阶段证据。

### 云连接、目录与本次对象

云仅支持静态env_ak／env_sts：AK模式填ID和Secret，STS模式还需Token，可填已知到期；没有自动续期／AssumeRole／实例元数据或默认profile。**云URL不用填**，由`CFW_CLOUD_CONTROL_PLANE_REGION`与允许地域映射CFW／ECS／STS受控端点。身份读取先核期望账号与受支持身份；connection_check仍只有IDENTITY_ONLY partial，不证明产品权限。

连接提供单账号、有限地域与边界的读取上界。allowed_assets／allowed_nat_gateways仅可选收紧，无需逐台登记；对话提供名称／ID／类型／地域等信息，目录定位，本次计划决定对象。接受有限资料准备计划后，同一audit先核缓存、必要时有界metadata与采集，再一次封存规则／Agent输入。歧义或未穷尽停在澄清；ECS批量分页、SG去重；NAT只在CFW管理目录内精确定位，一个任务绑定一个网关。云写工具不存在，用户人工应用策略。

首轮非fatal失败在身份齐备时保存明确partial输入及已得ACL／SG；fatal、取消或不足只保存安全观察与受控原因。独立collect／pinned历史入口仍保留。

### 执行保护与持久授权

四类完整profile由统一配置生成，计划、Store、Engine、Runner、Collector和HTTP共用；独立逻辑模型次数、物理尝试、单次和阶段时限、工具／批量、对象与计算保护逐层取严。默认工程值尚待真实校准，不是厂商SLA或调用授权。数据复用保持原时点，force_refresh／人工变更不偷用旧资料，历史解释零云。

云和模型共用一个Manager、application-root持久授权和attempt账本。共享初始预算只有一处配置，不再分别在云／模型JSON填额度。普通启动只初始化或收紧，已用量不会因改密钥、换ref、重启或新会话重置。增加额度／续期须停止持库锁的服务，使用`--budget-change`显式追加同ref修订，保留base CAS／change_id幂等和旧attempt。当前任务仍需明确接受，发送前预记每个物理尝试，包含重试；未知送达仍计已用。

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
