# 人工文档契约样例

本目录是documentation-only人工数据，非正式接口返回、规则结果或运行测试。`audit-contracts.json`与`frontend-events.json`对应已批准v0.6；`concurrency-and-api.json`描述3会话、简单SQLite WAL与生成接口文档初值。

现有文档校验器检查结构、范围、预算、版本和样例引用，不执行规则、SQLite、真实HTTP、模型或云。实际前后端契约在工程实现后生成并通过集成验证；不能把这些fixture计入20～30正式案例或真实验收成绩。

2026-10-29目标改为统一服务端WorkloadProfile；audit-contracts中的30秒、4／6次、100次／90秒及固定阶段组合保留为历史未校准契约示例／当前实现硬值，不是永久产品上限或真实性能。静态校验通过仅证明这组人工示例内部一致，不证明统一配置已经落地。目标见[会话执行](../../design/session-and-execution.md)，实际差异见[台账](../../development/implementation-gap.md)。
