# 换环境开发交接

交接日期：2026-10-21。本文是当前阶段的交接快照，不是正式系统安装说明。先读 [协作说明](../AGENTS.md) 和 [README](../README.md)。

## 1. 当前已经做到哪里

| 内容 | 状态与边界 |
| --- | --- |
| 需求、架构、实验与选型背景 | 已整理；正式语言、框架和首版具体边界仍需确认 |
| Go + Eino 最小集成 | 已实现独立实验，实际使用 Eino 执行器，模型是确定性脚本，数据是人工构造 |
| 离线测试 | 已验证多轮工具回传、证据、权限、预算、错误、取消和超时；此前普通测试、竞态检查和静态检查通过 |
| 参考资料 | 11 篇阿里云官方正文、来源与校验清单已随 Git 保存，不必重新寻找这些正文 |
| 模型私有配置 | 已实现 `.env.local`/环境变量读取和脱敏 `config-check`；它不发送请求 |
| 真实模型适配器 | **尚未实现，也未验证任何真实模型服务**；`chat_completions`/`responses` 配置标记不等于兼容 |
| 真实云平台 API | **尚未调用或验证**；没有创建云资源、打开日志或修改策略 |
| 正式业务系统 | 尚未实现完整规则引擎、后端、Web 或 TUI；实验不等于正式系统 |

结论只能是：Go + Eino 的小型本地执行机制可运行。不能据此声称模型能自主规划、推荐质量提升、云采集成功或实际网络可达。

## 2. 关键入口

- [阶段计划与已确认决策](05-roadmap-and-decisions.md)：区分用户确认、候选方案和后续阶段。
- [技术背景与选型](07-technical-background-and-options.md)：Go/Python/TS、框架、自研边界、未来 TUI。
- [云 API 与能力映射](08-cloud-integration-and-capability-map.md)：只读采集分层、字段和未支持语义。
- [实际验证记录](09-offline-validation-record.md)：执行过什么、结果是什么、没有验证什么。
- [实验运行与配置说明](../experiments/eino-offline/README.md)：具体命令及模型配置行为。
- [离线参考资料](references/README.md)：11 篇正文和校验清单。
- [模型配置空白模板](../.env.example)：可以提交，但不能填真实密钥。

## 3. 哪些东西不会随 Git 迁移

- `.env.local`、进程环境变量及任何模型/云凭据：需要在新环境重新填写，或经用户认可的安全方式单独转移。不要为了迁移而提交密钥。
- `.git/config` 中的仓库级用户名、邮箱：新 clone 后需重新设置，不能继承机器上的公司身份。
- `.cache/` 中的 Go module 与构建缓存：新环境首次运行 `prepare` 重新下载已锁定依赖。
- `data/reference-cache/` 的原始网页 HTML：未提交，但对应选定正文已在 `docs/references/`，不影响离线查阅。
- `data/raw/`、`data/private/`、`runtime/`、`reports/` 等私有数据和运行记录：继续排除。

本轮交接没有读取或确认私有配置是否已填写，不把“本地存在文件”写成“凭据可用”。

## 4. 新环境恢复步骤

### 4.1 获取代码并设置项目级隐私署名

在目标环境重新 clone 当前远程仓库；如果已有旧 clone，先确认没有未保存工作，不要把旧历史合并或推回当前主分支。

在仓库根目录设置，仅作用于本仓库：

```bash
git config --local user.name codelaod
git config --local user.email '26561324+codelaod@users.noreply.github.com'
git config --local user.useConfigOnly true
```

提交前同时核对 author 与 committer，防止环境变量覆盖仓库设置。不要修改全局 Git 配置，不恢复公司邮箱或系统默认身份。

### 4.2 恢复实验依赖并运行离线样例

已实测环境为 Linux/WSL、Go 1.26.10；module 声明 Go 1.24.0，其他 Go 版本未逐一验证。资料校验脚本使用 Python 3.9+ 标准库，已实测 Python 3.13.5。当前命令使用 Bash，未验证 Windows 原生命令等价实现。

```bash
# 仅首次准备需要联网，依赖版本已在 go.mod/go.sum 固定。
bash scripts/eino-offline.sh prepare

# 以下关闭模块联网，不访问真实模型或云平台。
bash scripts/eino-offline.sh test
bash scripts/eino-offline.sh vet
bash scripts/eino-offline.sh run

# 不联网、不安装 Python 包。
python3 scripts/check_references.py
```

`-race` 是可选补充，要求当前 Go 平台支持并具备 C 工具链：

```bash
bash scripts/eino-offline.sh test -race
```

不需要为了这一步安装前端 Node 依赖、数据库、Redis、Docker 或云 SDK。

### 4.3 恢复模型私有配置

仅当私有文件不存在时复制空白模板，然后由用户在本地编辑：

```bash
cp -n .env.example .env.local
chmod 600 .env.local
```

必填 `CFW_MODEL_BASE_URL`、`CFW_MODEL_API_KEY`、`CFW_MODEL_NAME`。不要把实际值写到 README、聊天或终端命令参数里，也不要用 `source` 执行私有文件。

填写后：

```bash
bash scripts/eino-offline.sh config-check
```

检查只输出字段是否已填、协议标记与限制，不输出实际地址、模型名和密钥。`request_sent=false`；`config_valid=true` 只证明格式有效，不证明服务连通或协议兼容。同名进程环境变量优先于文件，显式空值也会覆盖文件。

## 5. 下一步任务：真实模型 + 人工样例

1. 先用脱敏入口确认参数；不要直接输出私有文件。
2. 根据用户实际服务确认协议，**实现真实模型适配器**。目前不存在可直接调用真实模型的 `live` 命令，不要虚构命令。
3. 在用户确认调用与预算后，仅使用人工样例验证多轮工具调用、结果回传、结构化输出、失败和终止行为。填写密钥本身不是付费调用授权。
4. 配置中的调用次数、输出 token 和超时目前只是读取/校验参数；真实适配器必须实际应用限制，不能据此声称已有真实成本控制，也不能把它们说成金额上限。
5. 保留规则原始发现、证据编号、脱敏事件和错误；不存储完整内部推理，不把脚本预设路径当真实模型自主决策证据。
6. 模型验证之后，另行确认阿里云账号、产品、地域和最小只读权限，再核实云防火墙及必要 ECS/安全组 API。不要自动开通资源、日志或执行云上写操作，不默认把真实云数据发送给外部模型。

用户希望先补足技术背景再收敛范围，论文大纲不急；不要在新环境又从头扩张为多云、多 Agent、完整网络拓扑或自动策略下发。

## 6. 给下一环境的简短任务描述

> 请先读 AGENTS.md、README.md 和 docs/10-environment-handoff.md。当前只有 Go + Eino 脚本模型的离线集成验证，以及私有模型配置的无请求脱敏检查；真实模型适配器和阿里云 API 还没有接入。参考资料已在 Git 中。继续时先检查项目级隐私署名，再按用户实际模型服务和确认的预算实现真实适配，只用人工样例验证。不要输出密钥、把配置校验当接入成功，或擅自调用付费模型与云上写接口。
