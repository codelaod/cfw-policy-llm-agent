# 官方资料与核实记录

资料核实日期：2026-10-20；离线资料归档更新：2026-10-21。下列链接作为后续方案设计的官方资料入口，不能替代本账号、产品版本和实际接口返回的验证。价格、版本与能力变化时重新查阅。

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
- 审计基线或规范的原始来源、版本、适用条目和可自动检查范围。
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
| A-11 | [DescribeInstances](https://help.aliyun.com/zh/ecs/developer-reference/api-ecs-2014-05-26-describeinstances) | 实例、公网地址、EIP、安全组和网卡关联线索 |
| A-12 | [DescribeSecurityGroupAttribute](https://help.aliyun.com/zh/ecs/developer-reference/api-ecs-2014-05-26-describesecuritygroupattribute) | ECS 安全组规则结构；多安全组、默认与状态语义仍需额外资料和测试 |
| A-13 | [DescribeEipAddresses：旧版官方入口](https://help.aliyun.com/en/vpc/api-describeeipaddresses) | EIP 绑定线索与可下载性；不作为当前 SDK 兼容性证明 |
| A-14 | [阿里云官方 Go 云防火墙 SDK](https://github.com/alibabacloud-go/cloudfw-20171207) | 核实官方仓库存在且未归档；未安装编译或冻结版本 |
| A-15 | [阿里云官方 Go ECS SDK](https://github.com/alibabacloud-go/ecs-20140526) | 核实 Go 的 ECS 接入路线，不因此开放 SDK 的写方法 |
| A-16 | [阿里云官方 Python SDK](https://github.com/aliyun/alibabacloud-python-sdk) | Python 候选路线；未在本项目验证具体服务包 |

## 5. 语言、Agent 框架与终端入口

均为项目官方文档或源码。仅核对接口与设计能力，尚未安装运行、测试兼容性、冻结版本或完成依赖许可审查。

| 编号 | 一手资料 | 用途 |
| --- | --- | --- |
| M-03 | [OpenAI Agents SDK](https://developers.openai.com/api/docs/guides/agents-sdk/) | Python/TypeScript Agent SDK 候选，和模型 API SDK 分层看待 |
| M-04 | [OpenAI 官方 API SDK](https://developers.openai.com/api/docs/libraries/) | 核实包括 Go 在内的模型 API 接入路线；不等于官方 Go Agent 框架 |
| F-01 | [Eino ChatModelAgent](https://www.cloudwego.io/docs/eino/core_modules/eino_adk/agent_implementation/chat_model/) | Go 模型—工具循环与配置入口 |
| F-02 | [Eino ADK ChatModelAgent 源码](https://github.com/cloudwego/eino/blob/main/adk/chatmodel.go) | 核对迭代上限等实际配置；源码会变化，最终选型时再固定版本 |
| F-03 | [Eino OpenAI 组件源码](https://github.com/cloudwego/eino-ext/blob/main/components/model/openai/chatmodel.go) | 核对 BaseURL、超时/HTTP 客户端配置，不据此宣称任意服务兼容 |
| F-04 | [PydanticAI Agents](https://ai.pydantic.dev/agents/) | 类型化依赖、工具、输出和用量限制 |
| F-05 | [LangGraph Overview](https://docs.langchain.com/oss/python/langgraph/overview) | 有状态编排、持久执行、流式与人工参与的适用场景 |
| F-06 | [Google ADK Go 快速开始](https://google.github.io/adk-docs/get-started/go/) | Go Agent 的另一条候选路线；未在本项目验证实际模型适配 |
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
