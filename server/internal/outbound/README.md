# E3 HTTP准入与持久尝试

`Manager`由服务端显式配置固定source、origin、HTTP方法／路径及云／凭据action；每应用共享一个实例。每任务只通过`Client(jobContext, jobID, sourceRef)`取得独立绑定的`*http.Client`。模型当前固定alias为`configured-model`，与输入`DataSourceID`分开，配置修订仍须匹配已接受计划。

构造不访问网络、凭据或账本。真正`RoundTrip`先校验目的地、取得全应用cloud／credential共享2槽或model共享3槽（model每任务1），再由Store在短事务检查running状态、原deadline、效果和任务／会话／应用更严次数。预记提交成功后才发送；等待使用原任务context，取消或耗尽不能获得新次数。

`storage.migrateBudgets(tx)`已接入business schema 3显式迁移；旧库须停机备份后迁移，普通打开拒绝旧版或缺必要表／usage列的非完整schema 3，不在启动时自动修库。`ConfigureBudgetPolicy`是服务端授权期配置；同一Ref仅可收紧次数／到期，不清理历史，不因新会话、重启、重试、key轮换创建新周期。未配置、过期或0上限都拒绝请求。`reserved`和送达未知保守占用，不返还；晚到的受控结果可补记，但不改任务终态。次数不是金额，未记录供应商usage时不声称token或费用为0。

为保证一次guard调用就是一次物理HTTP，底层固定HTTP/1、关闭连接复用、代理及自动压缩。SDK重试仍需重新进入guard并再次预记；禁止重定向，响应解码前默认上限2MiB，错误正文替换为固定摘要。模型／云适配器必须只使用`ErrorCode(err)`作为持久或公开错误投影；`http.Client`及SDK的外层错误仍可能附带私有URL，不可保存`err.Error()`。

HTTP只允许显式loopback测试；生产来源需要HTTPS。生产cloud与credential RPC需要固定action白名单；无action的凭据交换只供loopback人工协议测试，尚未声称正式RAM／STS／实例角色适配通过。工具不能取得Manager或任意URL入口。

模型成功HTTP响应在SDK解码前只提取明确的`usage.prompt_tokens / completion_tokens / total_tokens`。三个字段必须都在场、非负且可精确表示；缺失、部分、负数、畸形或重复字段均保持未知。明确报告的三个0仍是已知值。原始响应不存入账本，结果与受控usage同一短事务保存；正常兼容接口也不会把未知改成0。

`BudgetUsage`同时返回已知usage响应数、未知usage尝试数和已知部分的reported总量；无已知响应时总量为null。汇总越过JSON安全整数边界时总量为null且标overflow，逐次报告值仍保留。不重算供应商total、不用这些reported计数推导账单或金额上限。

当前验证只使用httptest与假凭据，包括固定CFW SDK重试／人工凭据交换的持久计数、取消、共享并发、usage存在／缺失／明确0／畸形／失败、重启保留和schema 3显式迁移幂等与非完整库拒启。没有读取私有配置、调用真实云／模型或验证真实费用。
