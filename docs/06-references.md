# 官方资料与核实记录

初始资料核实日期：2026-10-20；归档、模型接入及需求 v0.3 调研补充：2026-10-22。下列链接作为后续方案设计的官方资料入口，不能替代本账号、产品版本和实际接口返回的验证。价格、版本与能力变化时重新查阅。

## 1. 云平台资料

| 编号 | 官方资料 | 本次用途与边界 |
| --- | --- | --- |
| A-01 | [阿里云云防火墙文档](https://help.aliyun.com/zh/cloud-firewall/) | 确认产品能力类别与资料入口 |
| A-02 | [DescribeAssetList：查询资产列表](https://help.aliyun.com/zh/cloud-firewall/cloudfirewall/developer-reference/api-cloudfw-2017-12-07-describeassetlist) | 资产查询的候选接口；后续需验证公网资产与保护状态字段，不预设完整替代 ECS 资产 API |
| A-03 | [DescribeControlPolicy：查询访问控制策略](https://help.aliyun.com/zh/cloud-firewall/cloudfirewall/developer-reference/api-cloudfw-2017-12-07-describecontrolpolicy) | 互联网边界策略读取的候选入口；方向、分页、引用对象和返回字段须实测 |
| A-04 | [云防火墙按量付费 2.0](https://help.aliyun.com/zh/cloud-firewall/cloudfirewall/product-overview/pay-as-you-go) | 核对实例费、处理流量费、功能费与释放规则；账号若使用旧版则查对应说明 |
| A-05 | [配置互联网边界访问控制策略](https://help.aliyun.com/zh/cloud-firewall/cloudfirewall/create-inbound-and-outbound-access-control-policies-for-the-internet-firewall) | 官方描述默认放行、策略顺序、观察动作与智能推荐；规则及建议必须考虑实际匹配语义 |

初始阶段仅确认官方文档入口，不代表已调用接口。本轮补充了云防火墙地址簿、ECS、安全组等文档与字段映射，并下载相关公开页面；真实账号权限、返回数据、分页、计数窗口与产品版本能力仍待最小验证。详见 [云平台接入与能力映射](08-cloud-integration-and-capability-map.md)。

产品自身已有“智能策略推荐”（A-05），其输入、可用版本和使用方式，与本论文拟实现系统的范围需要分别核对；文档入口的存在不能作为两者效果相同或本系统更优的证据。

## 2. 模型与工具资料

| 编号 | 官方资料 | 本次用途与边界 |
| --- | --- | --- |
| M-01 | [OpenAI Chat Completions 接口](https://platform.openai.com/docs/api-reference/chat/create) | 建议作为首个兼容目标，待技术方案阶段确认；重点核对消息、工具定义、工具调用及结果回传，第三方服务的兼容程度须单独测试 |
| M-02 | [Claude Code 概述](https://code.claude.com/docs/en/overview) | 明确 Claude Code 所在的编码 Agent 工具层次，后续与直接模型 API 适配分别讨论 |

不在本阶段固定具体模型名或以某款编码工具代替系统自身的 Agent 设计。若增加 Anthropic 模型 API 适配，应补充官方 API 文档、认证方式、消息与工具协议的实际验证记录。

## 3. 后续需补齐的证据

- 真实账号可用的云防火墙版本、防护边界、只读权限和完整采集字段。
- 云防火墙与 ECS 的资产关联方式，及保护状态的含义。
- 所选模型服务的工具调用兼容性、输出稳定性、上下文限制和费用。
- 安全基线采用范围与例外仍需审核：本轮已核读 NIST 指南和厂商控制主题，细化映射见规则目录；监管规范或整体合规认证尚未选择。
- 最终技术框架的官方资料和最小验证结论。
- 与课题直接相关的论文和产品资料。开题时另做检索，当前不编造参考文献。

## 4. 本轮云平台补充资料

以下为 2026-10-20 的技术背景调研入口。A-02、A-03 与下列相关页面已进行正文缓存；下载不代表账号接入成功。EIP 入口较旧，实施前须核对当前 OpenAPI/SDK。

| 编号 | 一手资料 | 本轮用途与核实边界 |
| --- | --- | --- |
| A-06 | [云防火墙 API 概览](https://help.aliyun.com/zh/cloud-firewall/cloudfirewall/developer-reference/api-cloudfw-2017-12-07-overview) | 区分互联网、NAT、VPC 等边界能力，不把接口类别混用 |
| A-07 | [DescribeAddressBook](https://help.aliyun.com/zh/cloud-firewall/cloudfirewall/developer-reference/api-cloudfw-2017-12-07-describeaddressbook) | 地址簿类型、成员和动态引用信息，展开结果仍须实测 |
| A-08 | [云防火墙访问控制策略原理](https://help.aliyun.com/zh/cloud-firewall/cloudfirewall/how-access-control-policies-work-1) | 顺序、未命中默认放行、应用/域名等匹配语义 |
| A-09 | [配置严格和宽松引擎模式](https://help.aliyun.com/zh/cloud-firewall/cloudfirewall/configure-the-strict-mode-of-the-internet-firewall) | 解释应用/域名识别与引擎模式的影响；只读采集路径尚未验证 |
| A-10 | [通过安全运营 Agent 配置访问控制策略](https://help.aliyun.com/zh/cloud-firewall/cloudfirewall/configure-access-control-policies-through-security-operation-agent) | 核实原生大模型策略建议能力的存在和日志前提，不接受宣传效果为本项目实验结论 |
| A-11 | [DescribeInstances](https://help.aliyun.com/zh/ecs/developer-reference/api-ecs-2014-05-26-describeinstances) | 2026-10-22 补核名称／标签／OS／地址／安全组／网卡上下文；可作自动线索，不证明运行服务、角色或业务授权，账号尚未实测 |
| A-12 | [DescribeSecurityGroupAttribute](https://help.aliyun.com/zh/ecs/developer-reference/api-ecs-2014-05-26-describesecuritygroupattribute) | 2026-10-22 补核方向／协议／端口／动作／优先级与引用；允许入向配置可推断候选，实际组关联、分页与范围另验收 |
| A-13 | [DescribeEipAddresses：旧版官方入口](https://help.aliyun.com/en/vpc/api-describeeipaddresses) | EIP 绑定线索与可下载性；不作为当前 SDK 兼容性证明 |
| A-14 | [阿里云官方 Go 云防火墙 SDK](https://github.com/alibabacloud-go/cloudfw-20171207) | 核实官方仓库存在且未归档；未安装编译或冻结版本 |
| A-15 | [阿里云官方 Go ECS SDK](https://github.com/alibabacloud-go/ecs-20140526) | 核实 Go 的 ECS 接入路线，不因此开放 SDK 的写方法 |
| A-16 | [阿里云官方 Python SDK](https://github.com/aliyun/alibabacloud-python-sdk) | Python 候选路线；未在本项目验证具体服务包 |

## 5. 语言、Agent 框架与终端入口

均为项目官方文档或源码。除 Eino 及本轮固定的 OpenAI 兼容组件已有模拟/受限真实测试外，其余只核对接口与设计；不声称所有框架都已安装、兼容或完成许可审查。

| 编号 | 一手资料 | 用途 |
| --- | --- | --- |
| M-03 | [OpenAI Agents SDK](https://developers.openai.com/api/docs/guides/agents-sdk/) | Python/TypeScript Agent SDK 候选，和模型 API SDK 分层看待 |
| M-04 | [OpenAI 官方 API SDK](https://developers.openai.com/api/docs/libraries/) | 核实包括 Go 在内的模型 API 接入路线；不等于官方 Go Agent 框架 |
| M-05 | [MiniMax 官方 OpenAI 兼容接口](https://platform.minimax.io/docs/api-reference/text-openai-api) | 本轮真实接入核实消息回传、思考字段与长度限制；有项目自写摘要，不能视为所有模型已兼容 |
| F-01 | [Eino ChatModelAgent](https://www.cloudwego.io/docs/eino/core_modules/eino_adk/agent_implementation/chat_model/) | Go 模型—工具循环与配置入口 |
| F-02 | [Eino ADK ChatModelAgent 源码](https://github.com/cloudwego/eino/blob/main/adk/chatmodel.go) | 核对迭代上限等实际配置；源码会变化，最终选型时再固定版本 |
| F-03 | [Eino OpenAI 组件源码](https://github.com/cloudwego/eino-ext/blob/main/components/model/openai/chatmodel.go) | 核对 BaseURL、超时/HTTP 客户端配置，不据此宣称任意服务兼容 |
| F-04 | [PydanticAI Agents](https://ai.pydantic.dev/agents/) | 类型化依赖、工具、输出和用量限制 |
| F-05 | [LangGraph Overview](https://docs.langchain.com/oss/python/langgraph/overview) | 有状态编排、持久执行、流式与人工参与的适用场景 |
| F-06 | [Google ADK Go 快速开始](https://google.github.io/adk-docs/get-started/go/) | Go Agent 的另一条候选路线；未在本项目验证实际模型适配 |
| F-07 | [Eino OpenAI 兼容模型组件 v0.1.13](https://pkg.go.dev/github.com/cloudwego/eino-ext/components/model/openai@v0.1.13) | 本轮固定依赖的组件；结合 Eino v0.9.22 完成模拟与受限 MiniMax 实测 |
| T-01 | [Bubble Tea](https://github.com/charmbracelet/bubbletea) | Go 终端 UI 候选 |
| T-02 | [Textual](https://textual.textualize.io/) | Python 终端 UI 候选 |
| T-03 | [Ink](https://github.com/vadimdemedes/ink) | React/JavaScript 终端 UI 候选 |

## 6. 代表性论文与项目

这是选型相关的初步检索，不是完整文献综述。只陈述本轮实际读取的资料和可借鉴方法，不转用外部实验数值，不声称已复现。

| 编号 | 原始资料 | 本轮核实范围 |
| --- | --- | --- |
| P-01 | [Prowler 阿里云接入](https://docs.prowler.com/user-guide/providers/alibabacloud/getting-started-alibabacloud) | 官方文档中的云 API 只读采集路线 |
| P-02 | [Prowler ECS 服务源码](https://github.com/prowler-cloud/prowler/blob/master/prowler/providers/alibabacloud/services/ecs/ecs_service.py) | 实际读取实例、安全组及规则的实现；未运行检查 |
| P-03 | [NetConfEval: Can LLMs Facilitate Network Configuration? 作者公开论文](https://dejankosticgithub.github.io/documents/publications/netconfeval-conext24.pdf) | CoNEXT 2024 论文，核对任务分解和评估思路；[DOI](https://doi.org/10.1145/3656296) |
| P-04 | [NetConfEval 作者仓库入口](https://github.com/sferlin/NetConfEval) | 查看说明与实验组织，未安装运行或复现 |
| P-05 | [Natural Language Interface for Firewall Configuration](https://arxiv.org/abs/2512.10789) | 2025 年 arXiv 预印本；查看摘要和正文中的中间表示、解析/生成分层，不作已同行评审或阿里云适配声明 |
| P-06 | [Batfish 官方快照差异与可达性文档](https://batfish.readthedocs.io/en/latest/notebooks/differentialQuestions.html) | 网络配置验证与分析的职责边界；未核实原生阿里云云防火墙支持 |
| P-07 | [GOLLUM: A Framework for Automated Firewall Configuration Using Large Language Models](https://www.scitepress.org/Papers/2025/132219/132219.pdf) | 仅确认出版商检索条目；本轮全文获取未成功，待核读，不用于具体技术结论 |

## 7. Git 中的离线副本与使用约定

- 根据用户 2026-10-21 的要求，选定的 11 篇阿里云正文已归档到 [离线参考资料](references/README.md)，随 Git 保存，换机不必重新寻找。
- 来源、真实抓取日期、正文大小与 SHA-256 见 `docs/references/aliyun/2026-10-20/manifest.json`；可运行 `python3 scripts/check_references.py` 离线校验。
- 原始 HTML 仍在忽略目录 `data/reference-cache/2026-10-20/`，不是查阅正文的前提。自动提取文本不是官方 Markdown 导出，也不是官方离线发行包。
- 第三方资料保留来源与原版权归属，不因纳入本仓库而重新授权。只归档已选定的公开资料，不扩大为全站镜像；论文与框架未下载全文的仍仅提供索引。
- 按索引与关键词读取相关段落，不在每次模型请求中发送全部资料；缓存不保证零 token 或零费用。
- Go + Eino 的独立离线集成验证见 [实验说明](../experiments/eino-offline/README.md)；依赖由该 module 的 `go.mod`/`go.sum` 固定，不能把模拟模型实验当成真实模型或云接口验证。


## 8. 需求与架构文档方法参考

| 编号 | 一手资料 | 核实用途与边界 |
| --- | --- | --- |
| S-01 | [Philippe Kruchten：Architectural Blueprints—The “4+1” View Model of Software Architecture，作者稿](https://arxiv.org/abs/2006.04975) | 1995 年 IEEE Software 论文，作者于 2020 年归档；2026-10-22 核读。逻辑、进程、开发、物理视图与场景用于覆盖架构问题，不规定本项目必须拆成五份文档；本项目的组织建议见架构讨论第 8 节 |

本次仅保存来源索引与项目自写的设计交付说明，未将论文全文下载或重新分发。需求依据与缺口见 [领域需求](12-audit-domain-requirements.md)；当前没有选择或声称符合某个完整需求／架构标准。


## 9. 业务价值、安全指南、角色与日志资料（2026-10-22）

以下均为官方厂商、标准机构或作者公开资料。A-10 的当前入口已复核；原有 2026-10-20 快照保持原来源和日期，不冒称重新抓取。本轮归档 **19 份当前项目自写摘要**，含 A-10 复核，见 [清单](references/requirements-research/2026-10-22/manifest.json)；不是新增 19 篇官方全文，未下载／重分发整份标准、指南或论文。

| 编号 | 一手资料 | 使用范围及限制 |
| --- | --- | --- |
| A-17 | [阿里云：互联网边界防火墙](https://help.aliyun.com/zh/cloud-firewall/cloudfirewall/internet-firewall) | 防护开关／范围；只说明该层当前状态，不证明历史或全层防护。 |
| A-18 | [阿里云：入侵防御配置](https://help.aliyun.com/zh/cloud-firewall/cloudfirewall/prevention-configuration) | IPS 模式、误拦截与保护目标；实际只读获取路径未验证。 |
| A-19 | [阿里云：云防火墙日志字段](https://help.aliyun.com/zh/cloud-firewall/cloudfirewall/log-fields) | 当前字段手册针对日志分析采集的数据。访问流量中的 acl_rule_id 与 IPS 日志的 rule_id、uuid 等含义不同，不能混用。需保留日志类别、动作、时间及关联，零记录不证明无访问。 |
| A-20 | [阿里云：日志审计与日志分析的边界](https://help.aliyun.com/zh/cloud-firewall/cloudfirewall/log-audit) | 当前手册说明日志审计可查看最近七天；延长保存或导出原始数据需日志分析。可查询审计页面不等于具备原始导出能力。七天是该产品说明，不是本项目或 NIST 的统一观察／保留阈值；费用与账号条件另核实。 |
| A-22 | [阿里云：安全组检查](https://help.aliyun.com/zh/cloud-firewall/cloudfirewall/check-security-group-rules) | 该功能针对 ECS 安全组的静态风险，包含远程管理和数据库等公网开放主题。项目只借鉴风险主题；不能把安全组检查结果或其阈值原样用于 CFW ACL，也不能把所有检查当通用强制规范。 |
| A-24 | [阿里云：业务可视中的自定义分组](https://help.aliyun.com/zh/cloud-firewall/cloudfirewall/user-guide/custom-groups) | 官方东西向业务可视以业务区、应用组与应用组织访问关系，并可按端口区分同一 ECS 的不同应用。文档及版本适用性需区分；仅借鉴组织方法，不声称首版账号支持或调用分组写接口。 |
| A-25 | [阿里云：Web 应用防火墙 3.0](https://help.aliyun.com/zh/waf/web-application-firewall-3-0/product-overview/what-is-waf) | WAF 面向 Web 应用的 HTTP/HTTPS 防护。项目只对已确认的公网 Web 服务给适用性核查，考虑前置入口／回源；不套用于数据库，不认为能替代全部网络控制。 |
| B-01 | [NIST SP 800-41 Rev.1：防火墙及策略指南](https://nvlpubs.nist.gov/nistpubs/Legacy/SP/nistspecialpublication800-41r1.pdf) | 2009 年最终版，重点 §4 与 §5.2.3；原则依据，不伪造条文阈值。 |
| B-02 | [NIST SP 800-207：零信任架构](https://csrc.nist.gov/pubs/sp/800/207/final) | 2020 年最终版，资源／上下文视角，不声称完成零信任系统。 |
| B-03 | [AWS Security Hub CSPM：EC2 控制项](https://docs.aws.amazon.com/securityhub/latest/userguide/ec2-controls.html) | EC2.13／14／18／19 风险主题；不迁移 AWS 隐式拒绝及合规结论。 |
| B-04 | [阿里云：ECS 安全性与最佳实践](https://help.aliyun.com/zh/ecs/user-guide/best-security-practices) | 官方要求识别、定义和分类信息资产，明确被保护数据及访问目的；给出网络隔离、标签管理和配置审计等方向。用作资产画像与最小暴露依据，不继承页面全部能力、历史调查数值或产品效果。 |
| I-01 | [AWS：资源标签策略](https://docs.aws.amazon.com/whitepapers/latest/tagging-best-practices/building-your-tagging-strategy.html) | 标签策略与维护责任；具体画像字段及信任映射由本项目提议。 |
| I-02 | [Google Cloud：Firewall Insights](https://docs.cloud.google.com/network-intelligence-center/docs/firewall-insights/concepts/overview) | 配置与使用分析的产品参考，不迁移厂商窗口或自动删规则。 |
| I-03 | [Microsoft：使用安全 Copilot 理解安全建议](https://learn.microsoft.com/en-us/azure/defender-for-cloud/summarize-with-copilot) | 官方说明 Defender for Cloud 可通过 Copilot 将安全建议汇总为自然语言，辅助理解风险与决定整改方式；有产品和 SCU 等前提。不将其宣传效益或更广泛功能记作本项目已具备能力。 |
| A-27 | [DescribeNetworkInterfaces](https://help.aliyun.com/zh/ecs/developer-reference/api-ecs-2014-05-26-describenetworkinterfaces) | 必要时补实例／网卡／安全组关系，限制所选资产与地域，不默认做全网络拓扑；只核读文档，未调用账号 |
| P-08 | [ReAct：推理与行动结合的方法论文](https://arxiv.org/abs/2210.03629) | 模型与工具反馈的方法参考，不是安全基线或本项目效果证明。 |

规则的产品语义、安全原则、厂商控制主题、项目政策及业务授权须分别记录。出处存在不代表本项目符合全部标准；采用范围、角色确认和 M1 日志增强仍需用户审核。映射与判据见 [规则目录](14-security-baselines-and-rule-catalog.md)，行业与资产角色方案见 [研究说明](13-business-value-and-asset-profiles.md)。

需求 v0.3 已按用户要求移除云安全中心资料及接入／客户端依赖；A-23 编号停用、不复用。新增 A-11／A-12 的本轮自写核读摘要及 A-27，当前需求研究清单共 19 份。旧官方正文日期不变，真实接口与费用／账号能力不由文档存在性证明。


## 10. 系统设计一手资料（2026-10-22）

[设计摘要清单](references/architecture/2026-10-22/manifest.json) 保存 9 份自写核读摘要：下列 7 个新索引及已有 F-01／A-08 的本轮设计复核；不是官方全文，也不代表新增依赖已安装／构建。

| 编号 | 一手资料 | 本项目使用边界 |
| --- | --- | --- |
| D-01 | [Go net/http](https://pkg.go.dev/net/http) | 标准 HTTP／上下文与请求边界；不等于业务服务已实现 |
| D-02 | [Vue 3 官方指南](https://vuejs.org/guide/introduction.html) | 轻量组件与 TypeScript 方案，设计建议待 G2 |
| D-03 | [Vite 官方指南](https://vite.dev/guide/) | 前端开发／静态构建路线；实施时固定兼容版本／锁文件 |
| D-04 | [modernc.org/sqlite 维护者文档](https://pkg.go.dev/modernc.org/sqlite) | CGO-free 驱动路线；依赖配套与平台构建未验证 |
| D-05 | [SQLite Isolation](https://sqlite.org/isolation.html) | 单写事务、日志模式与隔离边界；小规模单机不默认 WAL／共享盘 |
| D-06 | [OWASP REST Security Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/REST_Security_Cheat_Sheet.html) | 访问／状态／输入控制依据，不等于安全认证 |
| M-06 | [OpenAI 官方工具调用指南](https://developers.openai.com/api/docs/guides/function-calling) | 应用执行工具与结果回传；第三方及协议字段兼容需单独验证 |

方案、版本和未验证点见 [系统设计](03-architecture-discussion.md)、[数据接口](18-data-and-api-design.md) 与 [规则／Agent](19-audit-and-agent-design.md)。MiniMax 的协议限制继续使用 M-05 与真实记录，不因 OpenAI 官方文档存在而推导任意兼容。


## 11. 设计 v0.2：框架、会话与外部认证资料

本轮新增 [10 份自写摘要及清单](references/design-revision/2026-10-22/manifest.json)，不复制官方全文，不等于已安装依赖／认证成功。Eino 另只读核对已缓存固定 v0.9.22 的 RunnerConfig／Run，业务存储与恢复能力分开。

| 编号 | 一手资料 | 使用边界 |
| --- | --- | --- |
| D-07 | [Gin 文档](https://gin-gonic.com/en/docs/) | Web API／中间件，非 Agent；不能照抄默认监听／日志 |
| D-08 | [GORM 连接](https://gorm.io/docs/connecting_to_the_database.html) | 默认 CGO 与社区纯 Go 路线区分，正式适配尚未构建 |
| D-09 | [libtnb/sqlite 维护者仓库](https://github.com/libtnb/sqlite) | 基于 modernc 的社区 GORM 适配，不是官方驱动／本项目已验证 |
| D-10 | [GORM 更新](https://gorm.io/docs/update.html) | 零值、显式字段与条件更新，不直接 Save 历史 |
| F-08 | [Eino 会话／存储示例](https://www.cloudwego.io/docs/eino/quick_start/chapter_03_memory_and_session/) | 业务 Store 管会话／消息，不规定数据库或 ORM |
| F-09 | [Eino Runner 与恢复](https://www.cloudwego.io/docs/eino/core_modules/eino_adk/agent_extension/) | CheckPointStore 可选，执行状态不等于产品历史 |
| A-28 | [Go SDK 凭据配置](https://help.aliyun.com/zh/sdk/developer-reference/v2-manage-go-access-credentials) | 显式身份／STS／角色路线，静态 token 不自动续期 |
| A-29 | [credentials-go 官方仓库](https://github.com/aliyun/credentials-go) | 选定 Provider 与链路，不复制打印密钥的示例或无条件默认链 |
| A-30 | [GetCallerIdentity](https://help.aliyun.com/zh/ram/developer-reference/api-sts-2015-04-01-getcalleridentity) | 核对调用身份／账号；不编造页面未透出的最小授权信息 |
| X-01 | [MCP 授权规范 2025-11-25](https://modelcontextprotocol.io/specification/2025-11-25/basic/authorization) | HTTP／stdio 与 token 受众边界，非首版必需、非云身份替代 |

用户使用流程、时效与跨入口引导是本项目需求／设计选择；源码／文档存在不证明产品已交付。见 [20](20-session-history-and-data-freshness.md)、[21](21-credentials-and-tool-auth-design.md)、[22](22-guidance-and-multi-entry-design.md)。


## 12. 2026-10-26：出向／NAT 范围与原型工具

新增 [8 份自写摘要与来源校验](references/scope-and-prototype/2026-10-26/manifest.json)，关联 [CR-1 复审](24-scope-expansion-and-prototype-review.md)。S-01 为互联网策略方向，S-02 为 NAT 边界 ACL，S-03～S-08 为设计技能、界面审查、Codex 技能、Figma、Stitch 和浏览器验证。

来源支持产品或工具的公开能力，不证明本项目已实现。技能仓库关注量不等于单项技能评分；Stitch 的 Google Labs 仓库说明不属于 Google 正式支持产品。没有进行云调用、模型调用、Figma／Stitch 登录或项目上传。
