# 开发交接与环境恢复

更新日期：2026-10-28。需求B2、整体设计v0.6已通过；E0依赖契约及[E1离线核心／持久闭环](../validation/evidence/e1-local.md)已有实际证据，E2正式离线服务／五视图已通过限定离线验证；E3受限Agent已通过限定离线验证，实际结果与待验边界见[E3记录](../validation/evidence/e3-local.md)。当前任务状态与授权见[项目决策](../project/decisions.md)。

## 1. 推荐恢复顺序

1. 读根目录[AGENTS](../../AGENTS.md)、[文档导航](../README.md)、实施计划和当前任务相关设计。
2. 先检查Git状态，保留已有未提交改动；旧提交／推送授权不重复使用。
3. 只运行当前任务需要的现有离线检查，不能把独立实验命令当正式系统命令。
4. E1规则、E2持久业务链和E3受限Agent已接通。先核对[E3记录](../validation/evidence/e3-local.md)的本地fixture与待验边界，完成尚可离线实施的适配和回归，再按计划推进E4；E4真实接入与真实整改尚未实现／验收，不能提前认定只剩外部阻塞。继续TDD与Review修复，不重开G2或扩存储框架；提交／推送仍核对当前明确授权。

## 2. 可复用的现有成果

| 成果 | 范围与入口 |
| --- | --- |
| Go＋Eino独立实验 | [实验README](../../experiments/eino-offline/README.md)；模拟协议与受限真实模型适配，不是正式后端 |
| 正式离线工程 | [后端](../../server/README.md)、[Web](../../web/README.md)、[实际API](../api/README.md)；E3已接正式Eino Runner、固定工具和持久账本，E0人工诊断保持隔离 |
| 已执行证据 | [离线记录](../validation/experiments/offline-eino.md)、[模型初测](../validation/experiments/live-model.md)；不扩大已有结论 |
| 离线原型 | [原型README](../prototypes/audit-workflow/README.md)；人工数据、内存模拟，不验正式3会话或SSE |
| 一手资料 | [资料索引](../references/README.md)及manifest；可离线核对，不等于目标账号能力 |

## 3. 不随Git迁移的私有内容

`.env.local`／进程环境、云／模型凭据、`data/raw/`、`data/private/`、`runtime/`、私有数据库／备份／未脱敏报告不进入Git。新环境没有私有配置时只能恢复空白模板，由用户在本地填写；不要输出真实值、密钥片段或带密钥的URL，不从机器其他profile兜底。

正式服务不读取`.env.local`或实验默认配置；显式`--model-config`选择私有JSON，只读取其`api_key_env`指定的环境变量。`--config-check`仅本地输出脱敏摘要，启动和预览不外呼；configured／enabled不证明兼容或产生付费授权。保留原MiniMax模型和512等更严输出／次数限制，不借文档默认上限抬额。已有实验仍只通过`bash scripts/eino-offline.sh config-check`查看其摘要。缺模型／云配置不阻止离线规则、人工确认与文档工作。

## 4. 当前可运行的检查

在仓库根目录：

```bash
python3 scripts/check_design_docs.py
python3 scripts/check_references.py
node docs/prototypes/audit-workflow/prototype-smoke.cjs
```

已有Eino实验准备、离线运行和已授权真实测试的详细边界只维护在实验README，不在此复制另一套安装／启动说明。真实模型／云预算和资源权限必须明确；不自动重放请求或继承历史一次性授权。
