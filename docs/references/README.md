# 离线参考资料

这些选定资料根据用户要求纳入 Git，用于换机器后继续查阅，不需要重新寻找原文。归档日期为 2026-10-21，页面抓取日期为 2026-10-20；两者不是同一概念。

## 范围与来源

- 当前共 11 篇阿里云公开官方文档正文，来源 URL、标题、抓取日期与 SHA-256 见 [归档清单](aliyun/2026-10-20/manifest.json)。
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
