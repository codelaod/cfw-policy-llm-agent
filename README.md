# 基于大模型智能体的云防火墙策略审计与推荐系统设计与实现

以云防火墙策略审计为业务主线，以受限大模型Agent完成工具选择、证据补查、解释与条件推荐；结果由人审阅并在云端手动整改。

**当前进度：需求v0.6／B2与整体设计v0.6已通过；E3受限Agent已通过限定离线验证；E4当前切片已通过限定离线适配验证。** 正式Eino单任务Runner、九个固定资料工具、有限会话上下文、校验结果与共享物理HTTP账本已接业务服务／Web；原规则结果独立保留。当前E4切片与待验项见[E4适配记录](docs/validation/evidence/e4-adapters-local.md)，E3历史结果见[E3记录](docs/validation/evidence/e3-local.md)，前序见[E0](docs/validation/evidence/e0-local.md)／[E1](docs/validation/evidence/e1-local.md)／[E2](docs/validation/evidence/e2-local.md)。真实云、上游MiniMax连续兼容、正式评价与真实人工整改仍未验收。

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

- [正式后端](server/README.md)／[五视图Web工作区](web/README.md)：显式只读采集／身份检查、固定快照、会话／确认／计划、三任务槽位、有界Agent、持久事件／用量和报告复审；E0诊断页独立保留。
- [E1离线业务链](docs/validation/evidence/e1-local.md)：实际规则→保存发现／证据→审阅→公开JSON／Markdown→复审比较；三份[公开人工输入](testdata/public/README.md)。
- [实际API规范](docs/api/README.md)：固定Swag生成Swagger JSON／YAML，覆盖当前已注册业务接口与E0隔离诊断接口，另附八类事件schema。
- [Go＋Eino独立实验](experiments/eino-offline/README.md)：模拟模式、受限真实模型适配、私有配置规则及实际验证边界。
- [离线交互原型](docs/prototypes/audit-workflow/README.md)：原生HTML／CSS／JS、人工数据、内存事件模拟，不是正式组件工程。

仓库根目录可运行的文档／原型检查：

```bash
python3 scripts/check_design_docs.py
python3 scripts/check_references.py
node docs/prototypes/audit-workflow/prototype-smoke.cjs
```

当前离线检查与构建：

```bash
bash scripts/e0.sh test
bash scripts/e0.sh build
bash scripts/e0.sh api
cd web
npm ci
npm test
npm run build
```

本机启动、business schema 4显式迁移和应用访问口令见[后端说明](server/README.md)。默认不配云／模型即可使用离线规则；正式服务只通过显式`--model-config`／`--cloud-config` JSON和其中指定的环境变量取得配置，不读取dotenv、机器profile或已有实验私配。云适配当前仅env_ak／env_sts静态凭据，固定生产端点与expected account先行核对；不实现角色自动续期或discover_region。`--config-check`仅在本地输出脱敏摘要；启动／预览零外呼，只有接受计划或明确发送后才可能执行受共享预算约束的请求。云／模型同时启用须共用预算周期并取严，显式零模型额度保持零。采集先产生新快照，再由用户明确启动pinned审计；身份检查不等产品权限通过。配置存在或enabled不等于上游已验证或付费授权，既有MiniMax模型与512等更严私有上限保持。独立实验的[.env.example](.env.example)及`bash scripts/eino-offline.sh config-check`仍只适用于该实验。

许可证：[Apache License 2.0](LICENSE)。
