# 公开人工输入

这些JSON是单个虚构资产在三个所审分区下的独立构造输入，包含明确的时点、版本、来源、完整性和业务要求。它们不含真实账号／云配置，也不是正式20～30案例评价集；文件数不代表云实例数量。

| 制品 | 人工设定与预期 |
| --- | --- |
| [internet-in.json](internet-in.json) | HTTPS必要访问保留；管理服务全地址放行超出明确来源；数据库实际端口15432明确不需公网却配置放行；一对HTTPS精确重复候选。CHK-07／CHK-08和重复候选应可定位，不能统一关闭同机Web。 |
| [internet-out.json](internet-out.json) | 固定源地址，允许目标198.51.100.0/24超出业务/25；明确必要目标203.0.113.9被本层拒绝。OUT-01与OUT-02分别保留，不能反套入向高风险端口基线。 |
| [nat-out.json](nat-out.json) | 同样的外联差异，另有明确人工设定的网关／资产／观察位置关联。NAT-01通过仅代表该人工关联设定，NAT-02复用出向判据；不推演SNAT／DNAT。 |

内置入口为`audit.SyntheticExample("internet-in" | "internet-out" | "nat-out")`，纯Go构造，不读私有文件或调用外部服务。JSON与内置构造由测试逐项比较。普通上传即使保留`source_kind: synthetic`，`DecodeImport`也会强制归为`imported`，结论只能是带假设的导入条件观察。

所有CIDR／地址、角色、授权、保护与NAT关联都是案例设定；策略配置允许不证明运行时监听、真实路由或业务授权。入向示例保留CFW的默认允许语义，通过明确的末尾drop控制其他查询；不能把CFW默认套成安全组默认拒绝。

显式再生制品（仓库根目录，仍然只生成这三个公开人工文件）：

```bash
cd server
CFW_WRITE_PUBLIC_FIXTURES=1 CGO_ENABLED=0 go test ./internal/audit -run TestPublicExamplesMatch -count=1
```

平常运行测试不会写文件。更改案例设定时同步审阅实际规则判定，不把调试重复计为新独立案例。

## 公共单播配置地址的固定分类

CHK-07／CHK-08使用随源码嵌入的[IANA IPv4 Special-Purpose Address Registry CSV](https://www.iana.org/assignments/iana-ipv4-special-registry/iana-ipv4-special-registry-1.csv)，本次核对日期为**2026-10-28**，官方登记页的Last Updated为**2025-10-09**。固定数据位于`server/internal/audit/data/iana-ipv4-special-2026-10-28.csv`，分类版本为`iana-special-2025-10-09-read-2026-10-28-v1`；运行时不下载或查询路由。

按登记表的更具体前缀优先处理Globally Reachable属性。明确False的私网、共享地址100.64.0.0/10、基准198.18.0.0/15、三个文档网段、保留等特殊空间从公共单播配置候选范围排除；192.0.0.9/32、192.0.0.10/32以及登记为True的AS112／AMT等例外保留。224.0.0.0/4依[RFC 1112](https://www.rfc-editor.org/rfc/rfc1112)和[IANA多播登记](https://www.iana.org/assignments/multicast-addresses/multicast-addresses.xhtml)作为非单播域排除。CHK-07的宽泛路径若只剩明确非公共空间，不报告公网高风险；通用IPv4内核与CHK-01／03等业务集合比较不自动套用此过滤。

192.88.99.0/24的Deprecated条目Global Reachable为空，不能当False；若只有这种分类未知范围被允许，CHK-08保持信息不足，不报告确定公网暴露或整体安全，其中更具体的192.88.99.2/32仍按明确False处理。其余未列为特殊用途的单播空间只是本项目的公共配置查询子域，**不证明地址已分配、存在路由、端到端可达或真实来源能访问**。后续更新登记数据应同步审阅分类版本和内核版本，不静默改变已冻结报告的计算条件。
