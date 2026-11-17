# Go + Eino 历史离线技术验证

这是独立实验module，不是正式审计后端。2026-11-17配置统一后，实验仅保留人工样例／脚本模型与本机HTTP协议测试；旧私配解析、config-check和真实模型CLI入口已删除。实际应用的模型／云／执行配置和本地检查统一见[后端说明](../../server/README.md)与[唯一模板](../../.env.example)，不再维护第二套字段或默认值。

2026-10-22曾完成两个MiniMax人工样例真实调用，原范围见[历史初测记录](../../docs/validation/experiments/live-model.md)及[脱敏数据](../../docs/validation/evidence/live-model-2026-10-22.json)。历史结果不能证明连续多轮、完整协议、真实云或正式系统验收，也不产生新调用授权。

## 依赖与运行

独立go.mod／go.sum固定Eino及模型组件版本。首次prepare显式下载依赖到项目.cache；其余动作关闭模块代理／校验联网并禁用自动下载工具链。默认离线运行不需要数据库、Docker、云或模型凭据；协议测试只用进程内／loopback人工响应，不读取根目录.env.local。

```bash
bash scripts/eino-offline.sh prepare
bash scripts/eino-offline.sh test
bash scripts/eino-offline.sh vet
bash scripts/eino-offline.sh run
bash scripts/eino-offline.sh run -scenario whitelist
bash scripts/eino-offline.sh run -scenario missing-whitelist
```

-race另需平台和C工具链支持。脚本中的CFW_GO_PROXY仅控制prepare的Go依赖下载，不是服务运行配置；不能导入云／模型凭据到前端或使用机器默认身份。

[脚本报告](example-report.json)明确标记scripted_model，回答／路径为预设，不能作为自主选择质量证据。协议测试仍覆盖实际工具ID、多个工具分别回传、缺失usage、截断、超时、重定向、输出限制及敏感反射；内部完整消息只存内存，不输出reasoning。

## 源码职责

- fixture_model.go：人工脚本模型。
- probe.go：工具、范围、证据与结果校验及共用Eino执行。
- live_model.go／live_model_test.go：保留历史协议适配及本机人工HTTP回归，不再从外部配置创建真实调用入口。
- main.go：仅人工场景入口。

正式Web／规则／持久化／并发与真实云验收均由正式工程及现行验证计划记录，不能由本实验推导。
