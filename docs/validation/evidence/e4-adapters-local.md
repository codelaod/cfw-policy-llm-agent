# E4只读适配切片本机实现与验证记录

日期：2026-10-28。状态：**E4当前切片限定离线适配验证通过**。本记录只确认已实现切片的本机人工协议验证，真实账号与完整能力未验收；机器可读状态见[e4-adapters-local.json](e4-adapters-local.json)。[E3](e3-local.md)及前序历史证据保持原结论，不回写为E4结果。G1／G2和业务边界不变。

## 当前实际切片

| 范围 | 实现与证据落点 |
| --- | --- |
| 显式云配置 | cloudconfig只读取--cloud-config JSON和点名环境变量，当前仅静态env_ak／env_sts；固定生产端点、expected account、有限边界／地域／资产／网关。无dotenv／默认profile／实例元数据、AssumeRole或自动续期。见[配置](../../../server/internal/cloudconfig/config.go)与[空白模板](../../../server/cloud-config.example.json)。 |
| SDK与身份先行 | aliyunapi仅固定CFW五操作＋ECS两操作＋GetCallerIdentity。产品读取前核账号及受支持RAMUser／AssumedRoleUser与ARN，root／空资源／类型冲突拒绝；安全组先从所选ECS关联后才可读。构造与本地config-check不查身份。见[适配器](../../../server/internal/aliyunapi/api.go)、[身份回归](../../../server/internal/aliyunapi/external_review_test.go)。 |
| 受限采集 | 当前仅显式assets或单NAT gateway_policy，不支持discover_region或空列表全量发现。两轮配置族读取保留页号／TotalCount类型、NextToken、引用、未知条件、窗口／完整性与一致性；默认动作unknown，不由同VPC生成NAT关联。见[Collector](../../../server/internal/aliyuncollector/collector.go)、[分页](../../../server/internal/aliyuncollector/pagination.go)。 |
| 保守规范化 | 地址簿AddressList与Addresses双表示冲突／畸形保持unknown，不挑一边证明完整。两轮保护状态冲突降unknown／PROTECTION_CHANGED，后轮未核实降PROTECTION_UNVERIFIED；状态一致而后续ACL失败仍保留独立保护事实。未知枚举、复杂条件与缺引用不静默删除。见[Review回归](../../../server/internal/aliyuncollector/external_review_test.go)。 |
| 一个共享根预算 | 云／凭据／模型来源合入一个Manager；同时启用的两份cfg预算Ref相同，模型次数与到期取严，显式0模型上限保持0。物理HTTP含身份请求，先事务预记再发送；新会话、换密钥或加适配不重置授权周期。见[运行时](../../../server/internal/app/model_runtime.go)、[运行时回归](../../../server/internal/app/cloud_runtime_test.go)。 |
| schema 4与原子结果 | 配置范围目录、CloudRecord及私有raw_artifacts显式迁移；原始响应按原bytes SHA-256并base64保存，不经JSON重序列化改写。网络工作在事务外，snapshot／sealed输入／云结果／公开应用消息／终态在短事务原子提交；取消／重启不自动重放。见[schema 4](../../../server/internal/storage/schema4.go)、[云结果](../../../server/internal/storage/cloud_results.go)。 |
| 采集与审计分开 | collect先保存新快照，用户随后另行预览并接受pinned audit；当前fresh audit通过这两步完成，不隐式调模型或自动审计。规则／报告复用相同不可变输入，不把当前业务头替换旧任务。见[CloudService](../../../server/internal/app/cloud_service.go)、[SDK到报告集成](../../../server/internal/app/cloud_integration_test.go)。 |
| 身份检查限定 | connection_check当前只GetCallerIdentity，保存identity_verified=true、product_ready=false、IDENTITY_ONLY与partial／CLOUD_LIMITED；不证明产品权限、资源可见性、字段／分页或真实STS兼容。 |
| API／事件 | GET /api/v1/data-sources/{id}/collection-options纯本地目录，identity_verified不因GET变真；任务结果新增cloud_result。stage_changed仅在云结果提交后含phase=L1、stage=cloud_result_saved、cloud_result_ref；事件类型仍八类。Raw、过滤器、真实账号／凭据不进入云产品结果或公开报告。见[实际API](../../api/README.md)、[事件schema](../../api/events.schema.json)。 |

## 实际验证结果

真实SDK→人工loopback HTTP→共享账本→Collector→数据库→独立审计／报告的应用回归已取得：单次互联网资产采集9个物理HTTP、NAT网关采集5个、identity-only检查1个。这些是特定人工fixture的实际次数，不是完整阶段总调用量、云产品请求或正式评价案例。

| 项目 | 状态／结果 |
| --- | --- |
| 当前定向SDK／HTTP／账本／采集／持久化→审计报告链 | 上述特定人工fixture回归通过，已包含于最终全量 |
| Go全量、vet与无CGO构建 | 368个顶层／876个含子项测试通过，0失败；go vet ./...与CGO_ENABLED=0构建通过 |
| 前端单元、类型检查与生产构建 | 68／68测试通过、0失败；类型检查与生产构建通过 |
| 浏览器／服务联调 | Chromium 155.0.8059.12，28组检查；1440／768／390px；4任务共15个本机cloud／credential HTTP、0模型 |
| 固定Swag生成／实际路由双向对账 | API版本0.4.0实际生成，42路径／50操作，路由双向对账通过 |
| 实际持久事件与当前schema核对 | 28条实际持久事件从SQLite导出通过Draft202012；同28条API分页制品亦全部通过当前schema |
| 独立Review及修复复验 | 身份类型／ARN、引用双表示、跨轮保护及已知凭据转义／数字反射修复已通过最终全量；应用Review目标包99顶层／172含子项通过、0失败 |
| 实际CLI本地配置检查 | 禁用云样例在无应用口令环境输出脱敏摘要成功；零外呼，未读取真实密钥／私配 |
| 文档／来源／JSON／需求追溯 | 静态文档／来源与diff检查通过；JSON／schema结构检查通过，不作为运行验收 |

当前源码另行带tag执行的TestE4BrowserFixtureSmoke（两边界）通过；它是额外冒烟，不重复计入368／876普通全量数量。

复现入口仍见[后端说明](../../../server/README.md)／[Web说明](../../../web/README.md)。启动、config-check、collection-options和计划预览零外呼；只有明确接受的有界任务才可执行。本文不读取或公开私有配置、原始数据库、临时访问口令或真实资源。

## 本机浏览器与安全回归

四个持久任务分别为identity-only检查、互联网采集、NAT网关采集和单独明确接受的rules-only审计。终态为1个partial、3个completed，物理cloud／credential尝试分别1／9／5／0，共15次；模型均为0。互联网与NAT分别保留两轮8／4条观测；另行审计保留insufficient_data 5／needs_confirmation 1／pass 2，不把未知改成通过。

- 身份任务无snapshot按钮；任务完成后collection-options仍identity_verified=false。选择、预览、任务完成不会自动切换快照、启动审计、创建业务草稿或确认。
- 用户明确选择返回snapshot，另行预览并接受pinned rules-only审计；该审计云／模型0外呼。范围切换清空旧对象／会话，原会话使用修订条件切换；迟到任务导航回归已修。
- 暂停观察、轮询和只读重开均零POST，用量不变；硬刷新要求重新解锁，原身份任务仍partial／1云／0模型。
- 表单和结果在1440／768／390px均无横向溢出；pageerror、浏览器跨origin HTTP、localStorage／sessionStorage条目均为0。两名只读Web Review未留下其他阻断项，14张截图及完整制品保留在忽略路径output/playwright，浏览器与主fixture均正常结束，临时token／state／stop已删除。

Review还覆盖已知应用／配置凭据向云协议和私有raw保存的反射：明文、JSON转义后的字符串及json.Number精确值均应拒绝；HTTP业务输入同样不允许已知数字形式凭据落库。拒绝范围是服务端已知敏感值，不宣称能自动识别用户随意贴入的一切未知秘密。测试使用人工secret与假凭据，没有读取真实密钥或私有配置。

## 来源保证与明确未覆盖项

当前E4 HTTP响应均为**人工loopback provider fixture**，使用假凭据及预置身份／CFW／ECS数据；没有真实阿里云或上游模型请求。采集输入的live_readonly标记只说明经过受控采集代码路径，不能把这些人工HTTP字节称为用户账号的真实云观测。SDK签名／传输／账本／分页／封存链路的人工协议成功，也不能证明目标账号的身份、权限、实际字段、产品就绪或费用。

当前未实现／未交付：discover_region、AssumeRole／ECS角色自动续期Provider、prefer_fresh缓存／定时采集、自动fresh audit编排等后续目标／可选入口；它们不产生额外账号请求授权。采集后不隐式自动审计。安全组只在实例关联后作只读上下文／私有原文，不证明监听、角色或业务授权，也不宣称完整安全组审计。NAT关联保持unknown，默认动作保持unknown；没有新增SNAT／DNAT转换API、跨账号或云写能力。

仍需明确账号／地域／资产／单NAT网关／API／数据／权限／预算后，逐项核验真实身份与静态STS、产品权限／字段／分页／高级条件、完整性和真实范围；上游MiniMax连续兼容、至少一次人工整改→重采集→复审、E5的20～30独立案例和公平三组比较、2 CPU／4 GiB标准性能均未验收。人工fixture或E3历史小样例不替代这些结果，也不预设Agent效果提升。
