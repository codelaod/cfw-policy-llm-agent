# E0 本机联调前端

此目录是正式 Vue 工程的最小起点，当前只提供 `/e0` 依赖与事件契约联调页。人工任务由后端运行，页面读取真实 HTTP／SQLite 事件；它不执行审计规则、真实云采集或模型请求，也不代表五个业务视图已实现。

## 本地运行

版本基线：Node **24.22.0**、npm **11.19.1**。直接依赖固定精确版本，传递依赖由 `package-lock.json` 固定。

```bash
cd web
npm ci
npm run dev
```

Vite 仅监听 `127.0.0.1:5173`，将 `/api` 代理至 `http://127.0.0.1:8080`，不新增 Node 业务服务。先按服务端说明启动后端，再在浏览器打开 `http://127.0.0.1:5173/e0` 并输入其应用访问口令。

```bash
npm test
npm run build
```

`build` 先运行严格类型检查再生成 `dist/`。`npm run preview` 仅预览静态构建，不代理 API；端到端联调当前使用开发代理，后端同源静态托管尚未实现。构建产物、依赖目录和自动生成的类型声明不进入 Git。

## 验证操作与边界

- “启动三会话联调”并行提交三个不同的人工会话，后台是否重叠以服务端运行记录为准。每个会话最多一个未结束任务，各自持有 job ID 和连续事件游标。
- “暂停观察”只关闭当前订阅；“恢复 SSE”和“使用轮询”仅发送 GET，沿用内存中最后连续 seq，不重建或取消任务。页面隐藏时停止观察，回来后人工恢复。
- 创建请求的结果未知时保留原 `client_request_id`，只能人工以原标识重试；刷新不自动重放任何 POST。重载后可粘贴已有任务 ID，从 seq 0 只读恢复。
- 应用访问口令只保留在当前页面内存，通过 `X-App-Token` 请求头发送；不进入 URL、构建环境、localStorage 或 sessionStorage。锁定、刷新和离页都会失去口令。
- fetch SSE 使用固定 `eventsource-parser`，检查 schema／job ID／事件类型／连续 seq；UTF-8 解码跨 chunk，支持 CRLF、多条 data 行与注释。协议错误停止观察，401 需重新解锁；SSE 连续三次网络失败转 JSON 轮询，二者不会同时读取。
- 单事件正文按 UTF-8 字节检查 4 KiB。解析库的缓冲上限以字符计，因此设置保守的 16 Ki 字符预算，使未完成帧字节缓存低于 64 KiB 上限。
- 请求超时与取消信号通过局部 `AbortController` 组合，退出时清理计时器和监听器，不依赖较新的 `AbortSignal.any/timeout`。
- 事件回放不能代替完整业务历史恢复。此阶段仅验证最小 E0 合约；会话消息、业务确认、审阅、报告及正式任务准入仍由后续阶段实现。

界面使用系统中文字体和本地 SVG 品牌图形，没有远程字体或图片；固定 `@iconify-json/lucide` 1.2.147 并启用 `icon.clientBundle.scan`，Nuxt UI 默认图标随构建打包。组件构建与浏览器网络验证的实际结果应记入 E0 证据；仅依赖安装或源码检查不能当作浏览器离线启动验收。

## 固定依赖来源

2026-10-28 从官方 npm registry 核对并锁定：Vue 3.5.44、Vue Router 4.6.4、Nuxt UI 4.12.0、Tailwind CSS 4.3.3、Vite 8.3.5、TypeScript 5.9.3、vue-tsc 3.3.13、eventsource-parser 4.1.3。

E0 首次构建发现 vue-tsc 3.3.13 无法调用 TypeScript 7.0.2 的 `typescript/lib/tsc` 导出入口（`ERR_PACKAGE_PATH_NOT_EXPORTED`），因此固定兼容的 TypeScript 5.9.3，不以移动 latest 作为工程版本。

- [Nuxt UI Vue/Vite 安装与配置](https://ui.nuxt.com/docs/getting-started/installation/vue)
- [eventsource-parser 源码与解析器说明](https://github.com/rexxars/eventsource-parser)
- [项目事件详细设计](../docs/design/frontend-and-events.md)

选定 Nuxt UI 的 Node 要求为 `^20.19.0 || >=22.12.0`，eventsource-parser 要求 `>=22.12`；本工程按上述 Node 24 基线验证。现代浏览器构建目标沿用 Chrome 111／Safari 16.4／Firefox 128，实际浏览器实测版本另记，不声称覆盖所有目标浏览器。
