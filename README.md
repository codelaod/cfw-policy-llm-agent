# 基于大模型智能体的云防火墙策略审计与推荐系统设计与实现

以云防火墙策略审计为业务主线，以受限大模型Agent完成工具选择、证据补查、解释与条件推荐；结果由人审阅并在云端手动整改。

**当前进度：需求v0.6／B2与整体设计v0.6均已审核通过，正式业务系统尚未实现，下一阶段为E0依赖与契约验证。** 已有Go＋Eino实验和离线原型只是前期证据，不代表正式后端、前端或真实云接入验收。

## 文档入口

完整分类与阅读顺序见 **[项目文档导航](docs/README.md)**。

| 目的 | 入口 |
| --- | --- |
| 看业务定位和当前决定 | [项目定位](docs/project/overview.md)／[批准决策](docs/project/decisions.md) |
| 看已确认需求与产品流程 | [系统需求](docs/requirements/system.md)／[PRD](docs/requirements/product.md) |
| 看整体方案与详细契约 | [系统架构](docs/design/architecture.md)／[数据与API](docs/design/data-and-api.md) |
| 开始下一阶段研发 | [实施计划](docs/development/implementation-plan.md)／[开发交接](docs/development/handoff.md) |
| 看验证要求和已有证据 | [验证计划](docs/validation/verification-plan.md)／[评价计划](docs/validation/evaluation-plan.md) |
| 查来源与论文材料 | [一手资料](docs/references/README.md)／[开题文稿](docs/thesis-proposal/README.md) |

## 已批准的边界

- 阿里云优先：互联网入向、有限出向、NAT边界ACL；单账号、显式有限地域／资产。不扩多云、复杂路径矩阵或完整SNAT／DNAT转换审计。
- 云端只读；人工确认业务资料、手动应用策略，再重新采集／复审。不接云安全中心，不做自动下发、多Agent协作或微服务平台。
- 单Go服务支持3个不同会话同时推进，同会话1个活动任务；纯Go SQLite WAL＋普通连接池，不为小数据量自研写调度或强制读写分池。
- Go／Gin／Eino；Vue／TS／Vite／Router／Nuxt UI v4／Tailwind；带应用访问头的fetch SSE＋轮询兜底。上游模型暂为非流式Chat Completions兼容适配。
- 20～30独立人工案例不等于VM数量；真实验证优先同一台ECS／同一套防火墙串行复用，至少一次真实人工整改复审不能用离线替代。

设计通过不等于实际功能或真实接口已验收，也不自动产生付费请求、云写、采购或后续提交推送授权。协作规则见[AGENTS](AGENTS.md)。

## 现有可运行内容

- [Go＋Eino独立实验](experiments/eino-offline/README.md)：模拟模式、受限真实模型适配、私有配置规则及实际验证边界。
- [离线交互原型](docs/prototypes/audit-workflow/README.md)：原生HTML／CSS／JS、人工数据、内存事件模拟，不是正式组件工程。

仓库根目录可运行的文档／原型检查：

```bash
python3 scripts/check_design_docs.py
python3 scripts/check_references.py
node docs/prototypes/audit-workflow/prototype-smoke.cjs
```

模型私有配置保留在被忽略的`.env.local`，空白模板为[.env.example](.env.example)；仅通过`bash scripts/eino-offline.sh config-check`查看脱敏摘要，不发送请求。正式业务系统尚无安装、构建或启动命令，不用实验命令冒充。

许可证：[Apache License 2.0](LICENSE)。
