# 官方资料与核实记录

核实日期：2026-10-20。下列链接作为后续方案设计的官方资料入口，不能替代本账号、产品版本和实际接口返回的验证。价格、版本与能力变化时重新查阅。

## 1. 云平台资料

| 编号 | 官方资料 | 本次用途与边界 |
| --- | --- | --- |
| A-01 | [阿里云云防火墙文档](https://help.aliyun.com/zh/cloud-firewall/) | 确认产品能力类别与资料入口 |
| A-02 | [DescribeAssetList：查询资产列表](https://help.aliyun.com/zh/cloud-firewall/cloudfirewall/developer-reference/api-cloudfw-2017-12-07-describeassetlist) | 资产查询的候选接口；后续需验证公网资产与保护状态字段，不预设完整替代 ECS 资产 API |
| A-03 | [DescribeControlPolicy：查询访问控制策略](https://help.aliyun.com/zh/cloud-firewall/cloudfirewall/developer-reference/api-cloudfw-2017-12-07-describecontrolpolicy) | 互联网边界策略读取的候选入口；方向、分页、引用对象和返回字段须实测 |
| A-04 | [云防火墙按量付费 2.0](https://help.aliyun.com/zh/cloud-firewall/cloudfirewall/product-overview/pay-as-you-go) | 核对实例费、处理流量费、功能费与释放规则；账号若使用旧版则查对应说明 |
| A-05 | [配置互联网边界访问控制策略](https://help.aliyun.com/zh/cloud-firewall/cloudfirewall/create-inbound-and-outbound-access-control-policies-for-the-internet-firewall) | 官方描述默认放行、策略顺序、观察动作与智能推荐；规则及建议必须考虑实际匹配语义 |

本次只确认接入存在官方文档入口，不代表已调用接口。ECS 详细信息、地址组、命中统计、流量日志、RAM 权限与具体版本能力，均在最小验证阶段补充对应接口和字段映射。

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
