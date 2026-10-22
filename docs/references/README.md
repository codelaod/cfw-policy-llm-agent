# 离线参考资料

本目录用于按用户要求保存适合纳入版本管理的选定资料，方便换机器后继续查阅；新增文件是否已提交以 Git 状态为准。阿里云正文于 2026-10-21 归档、2026-10-20 抓取；另于 2026-10-22 加入 MiniMax 协议自写核实摘要，两者均明确标注来源与日期。

## 范围与来源

- 包含 11 篇阿里云公开官方文档正文，见 [归档清单](aliyun/2026-10-20/manifest.json)；另有 1 份 [MiniMax 项目自写核实摘要](minimax/2026-10-22/openai-compatible-notes.txt) 和 [清单](minimax/2026-10-22/manifest.json)，不是 MiniMax 官方全文。
- 保留提取正文，不保存网页导航、脚本和大段页面渲染数据。原始 HTML 仍可保留在忽略目录 `data/reference-cache/`，不是跨机器查阅的前提。
- 正文中的地址或资源标识可能是官方接口示例，不是本项目采集的生产数据。
- 第三方资料保留其原有来源与版权归属；仓库的 Apache License 2.0 不自动重新授权这些原文。不把本目录表述为阿里云官方离线发行包，也不扩大成整站镜像。
- 这是特定日期的快照，不保证代表最新能力；EIP 入口较旧，已在清单标记。真实产品版本、账号权限和 API 返回仍须验证。
- 后续增加第三方全文前核对来源条款；没有成功获取或尚未确认可保存的论文只保留引用，不混写成已有离线全文。

## 本地校验

从仓库根目录运行：

```bash
python3 scripts/check_references.py
```

仅使用 Python 标准库，不联网、不安装包、不读取云或模型凭据。校验文件大小、SHA-256、来源信息和重复编号。

## 正文索引

| 快照 | 官方页面标题 |
| --- | --- |
| [cfw-api-overview](aliyun/2026-10-20/cfw-api-overview.txt) | 全部可调用接口功能参考大全-云防火墙-阿里云-云防火墙(Cloud Firewall)-阿里云帮助中心 |
| [cfw-describe-asset-list](aliyun/2026-10-20/cfw-describe-asset-list.txt) | 使用DescribeAssetList查询资产列表-云防火墙-阿里云-云防火墙(Cloud Firewall)-阿里云帮助中心 |
| [cfw-describe-control-policy](aliyun/2026-10-20/cfw-describe-control-policy.txt) | 使用DescribeControlPolicy查询访问控制策略-云防火墙-阿里云-云防火墙(Cloud Firewall)-阿里云帮助中心 |
| [cfw-describe-address-book](aliyun/2026-10-20/cfw-describe-address-book.txt) | 使用DescribeAddressBook接口批量查询地址簿列表-云防火墙-阿里云-云防火墙(Cloud Firewall)-阿里云帮助中心 |
| [cfw-internet-policy-guide](aliyun/2026-10-20/cfw-internet-policy-guide.txt) | 配置资产和互联网之间的双向流量的访问控制策略-云防火墙(Cloud Firewall)-阿里云帮助中心 |
| [cfw-policy-matching](aliyun/2026-10-20/cfw-policy-matching.txt) | 访问控制策略的流量匹配原理-云防火墙(Cloud Firewall)-阿里云帮助中心 |
| [cfw-strict-mode](aliyun/2026-10-20/cfw-strict-mode.txt) | 配置访问控制严格和宽松引擎模式-云防火墙(Cloud Firewall)-阿里云帮助中心 |
| [cfw-native-security-agent](aliyun/2026-10-20/cfw-native-security-agent.txt) | 通过安全运营Agent配置访问控制策略-云防火墙(Cloud Firewall)-阿里云帮助中心 |
| [ecs-describe-instances](aliyun/2026-10-20/ecs-describe-instances.txt) | 调用DescribeInstances API查询实例的详细信息列表-云服务器 ECS-阿里云-云服务器 ECS(ECS)-阿里云帮助中心 |
| [ecs-describe-security-group-attribute](aliyun/2026-10-20/ecs-describe-security-group-attribute.txt) | 使用DescribeSecurityGroupAttribute查询指定安全组的属性及规则-云服务器 ECS-阿里云-云服务器 ECS(ECS)-阿里云帮助中心 |
| [eip-describe-eip-addresses](aliyun/2026-10-20/eip-describe-eip-addresses.txt) | 调用DescribeEipAddresses接口查询指定地域已创建的EIP-阿里云帮助中心 |

## 更新约定

新抓取资料放到新的日期目录，保留旧快照，避免静默覆盖实验依赖的资料。记录真实抓取日期、原始来源和校验值；不要因为文件在 Git 中就认为可以省略来源核实。


## 当前需求调研摘要（2026-10-22，需求 v0.3）

当前 [清单](requirements-research/2026-10-22/manifest.json) 共 **19 份自写摘要**：v0.2 的 17 份中移除云安全中心候选摘要，补充 ECS／安全组／必要网卡的 3 份 API 核读摘要。A-23 编号停用，不在当前依赖或可选方案中；并未删除原有 11 篇官方正文。

资料涉及阿里云防护／日志／ECS 元数据、NIST 指南、AWS 控制与标签、Google 规则分析、Microsoft 安全辅助及 ReAct。每份保留一手 URL、日期、范围、字节数和 SHA-256，**不是 19 篇新增官方全文**。11 篇原正文＋1 份 MiniMax 摘要＋19 份需求摘要，共 31 个当前校验条目；是否已提交以 Git 状态为准。

产品使用方法见 [PRD](../16-product-requirements-and-user-flows.md)、[资产画像](../13-business-value-and-asset-profiles.md) 和 [规则目录](../14-security-baselines-and-rule-catalog.md)。接口字段已核读不等于账号权限／返回验收，不把配置端口当服务或授权证明。


## 系统设计摘要（2026-10-22）

新增 [9 份设计资料自写摘要及清单](architecture/2026-10-22/manifest.json)，包括 Go HTTP、Vue／Vite、SQLite／驱动、OWASP、工具协议、Eino 与 CFW 语义复核。保留原 URL、日期、范围与哈希，不是官方全文，不等于新依赖已安装或构建。

当前共有 40 个文件校验条目：11 篇原正文＋1 份 MiniMax 摘要＋19 份需求摘要＋9 份设计摘要；不等于 40 个不同网站或全部已提交。历史自审中的 31 是当时实际数量。设计与未验证点见 [系统设计](../03-architecture-discussion.md) 和 [B0](../17-requirements-cross-review-and-design-baseline.md)。


## 设计 v0.2 修订资料

[新增 10 份摘要清单](design-revision/2026-10-22/manifest.json) 涵盖 Gin、GORM／纯 Go 适配／更新语义、Eino 会话与 Checkpoint、阿里云凭据与身份、MCP 授权；均是项目自写核读摘要，不是官方全文或项目构建／认证结果。当前共 50 个校验文件：原 40 项＋本轮 10 项；旧记录的 31／40 是当时实际数量。是否提交仍由 Git 状态证明，哈希检查本身不证明已推送。
