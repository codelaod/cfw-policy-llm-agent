# 换环境开发交接

更新日期：2026-10-22。本文是当前阶段的交接快照，不是正式系统安装说明。先读 [协作说明](../AGENTS.md) 和 [README](../README.md)。

## 1. 当前已经做到哪里

| 内容 | 状态与边界 |
| --- | --- |
| 需求与领域需求 | v0.5／B1 已按用户条件授权和交叉审核作为设计输入；非逐项签字或业务验收 |
| 系统设计 | 系统架构、数据／API、规则／Agent 三份 v0.2 已形成，待用户 G2；正式业务未开工 |
| Go + Eino 最小集成 | 默认脚本模式，另有显式 live 模式；复用同一 Eino/领域工具，数据均为人工构造 |
| 离线测试 | 已验证多轮工具回传、证据、权限、预算、错误、取消和超时；此前普通测试、竞态检查和静态检查通过 |
| 参考资料 | 11 篇阿里云官方正文、来源与校验清单已随 Git 保存，不必重新寻找这些正文 |
| 模型私有配置 | 已实现 `.env.local`/环境变量读取和脱敏 `config-check`；它不发送请求 |
| 真实模型适配器 | 已实现受限非流式 Chat Completions；MiniMax 两个案例通过，仍未验证连续补查、流式或 Responses |
| 真实云平台 API | **尚未调用或验证**；没有创建云资源、打开日志或修改策略 |
| 正式业务系统 | 尚未实现完整规则引擎、后端、Web 或 TUI；实验不等于正式系统 |

结论包括本地执行机制可运行，以及 MiniMax 个例的工具批次/结果回传与本地 JSON 校验成功；不据此声称连续多轮自主规划、推荐质量提升、云采集或网络可达。

## 2. 关键入口

- [交叉审核／B0](17-requirements-cross-review-and-design-baseline.md)：用户条件授权、22 项发现与默认范围。
- [系统设计](03-architecture-discussion.md)、[数据／API](18-data-and-api-design.md)、[规则／Agent](19-audit-and-agent-design.md)：当前 G2 待审产物。

- [需求主文档 v0.5](02-requirements-and-scope.md) 与 [审计领域需求 v0.5](12-audit-domain-requirements.md)：B0 设计输入，具体默认与后续验证见交叉审核；设计仍待 G2；另见 [业务价值与画像](13-business-value-and-asset-profiles.md)、[细化规则](14-security-baselines-and-rule-catalog.md) 和 [需求自审](15-requirements-self-review.md)。
- [阶段计划与已确认决策](05-roadmap-and-decisions.md)：区分用户确认、候选方案和 G1／G2 两次审核门槛。
- [技术背景与选型](07-technical-background-and-options.md)：Go/Python/TS、框架、自研边界、未来 TUI。
- [云 API 与能力映射](08-cloud-integration-and-capability-map.md)：只读采集分层、字段和未支持语义。
- [初始离线验证记录](09-offline-validation-record.md) 与 [MiniMax 真实初测](11-live-model-validation.md)：区分本机模拟和真实调用证据。
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

## 5. 下一步任务：审阅系统设计 G2

1. 当前不是等待 G1 逐项回复：用户已给出条件授权，交叉审核修订后按 B0 进入 P2。先读 17 的默认／风险处置，再审 03／18／19。
2. 设计建议为 Go＋Gin＋Eino、Vue／TypeScript／Vite、GORM／纯 Go SQLite、一个工作器／轮询；没有创建这些正式工程或新数据库。新增依赖只核读文档，未安装／构建。
3. 用户 G2 审核前不创建正式业务脚手架、迁移、云实例或运行新的真实模型／云请求；本次授权不等于自动进入编码。
4. G2 改变范围／框架／M1 深度时同步需求 v0.5、B0 和设计；原型仅为产品交互样例，不包含新的访问／幂等／持久化控制。
5. 云安全中心继续明确排除；M1 默认关闭，真实只读采集与模型连续依赖补查按批准范围／预算后续验收。现有 4 次 MiniMax 请求不推广成正式效果或完整兼容。
6. 私有配置、原始云数据、模型正文、数据库和生成报告仍不进入 Git。脚本／模板当前仅为实验，不能向用户提供未实现的正式启动命令。

## 6. 给下一环境的简短任务描述

> 请先读 AGENTS.md、README.md、docs/17-requirements-cross-review-and-design-baseline.md 和系统设计 03／18／19。当前是 P2：用户授权交叉审核无关键阻塞后进入设计，需求 v0.5／B1 已成为设计输入，设计 v0.2 待用户 G2；不要回退到无限等待 G1，也不要自动开始业务编码。设计建议 Go／Gin／Eino＋Vue／TS／Vite＋GORM／纯 Go SQLite／单工作器，B0 默认 M0、M1 关闭、只读云，不接云安全中心。同时读取 20／21／22 的会话时序、统一认证和跨入口引导；先处理用户对架构／契约／算法与安全预算的意见；真实连续补查和云 API 仍未验收。凭据和真实原文不随 Git，资料／设计是否提交以 Git 状态为准。

本轮 B1 新增连续会话／历史、缓存时序与累计耗时、统一凭据及 Web／CLI／TUI 引导，见 20／21／22；Review 与发布核对见 23。用户已授权审查修订后提交推送，具体 SHA／远端结果以 Git 与最终回执为准。设计仍待 G2，不能根据发布成功自动编码、后台采集或调用模型。
