# Agent Box 第一版产品需求

- 状态：Active
- 产品版本：V1
- 关联技术设计：[`agent-box-technical-design.md`](./agent-box-technical-design.md)
- 关联 ERD：[`agent-box-erd.md`](./agent-box-erd.md)

## 1. 产品目标

用户可以通过 Tailscale 虚拟局域网，在手机或外部电脑的 Web Console 中持续访问运行在本机或服务器上的 Agent Box。Agent 在用户断开页面后继续工作，用户重新连接后可以恢复对话、查看事件并继续下达指令。

第一版围绕 OMP、Codex 与 Claude Code Runtime 提供完整可用产品；三种 Runtime 都可用于结构化后台执行和原生 Agent Terminal。

## 2. 第一版组件

必须以三个独立组件开发和测试：

1. `agentbox-web`：Web/PWA Console。
2. `agentbox-server`：始终在线、多用户 Control Plane。
3. `agentboxd`：运行在 macOS 或 Linux Execution Host 上的守护进程。

`agentbox-web` 与 `agentbox-server` 是独立开发组件；首版允许 Web 静态资源嵌入 Server 进行单点部署。

部署约束：除 `agentboxd` 外，Control Plane 组件统一由 Docker Compose 部署。`agentbox-server` 镜像包含已构建的 Web 静态资源；PostgreSQL 独立容器持久化。`agentboxd` 必须原生运行在 Execution Host，访问系统 OMP/Codex/Claude/tmux、项目目录和用户认证。

## 3. 网络与远程访问

- 所有 Client、Server 和 Host 位于同一个 Tailscale Tailnet。
- 第一版不实现公网 Relay，不要求公网 IP，不开放 OMP/Codex/Claude stdio Runtime 到网络。
- Web Client 仅访问 `agentbox-server`。
- `agentboxd` 主动连接 `agentbox-server`，Runtime 只在 Host 本地运行。
- 禁止依赖 Tailscale Funnel。
- Web Console 只支持 AgentBox 本地账号密码登录；Session 使用服务端持久化、HttpOnly、SameSite=Strict Cookie。Tailscale 只负责网络连通与设备级 ACL，不参与用户身份、角色或 Session 判定。

## 4. Runtime 范围

### 4.1 第一版 Runtime

- 对用户开放 OMP、Codex 和 Claude Code Runtime。
- OMP 使用 `omp --mode rpc`；Codex 使用官方 `codex app-server --listen stdio://` 协议；Claude Code 使用 stream-json 协议。
- 三种 Runtime 都直接使用 Execution Host 系统用户现有认证，不由 Agent Box 管理账号或 API Key。
- 支持多轮 Prompt、Interrupt、Stop、Resume 以及 Runtime 实际声明的能力；不支持的 Steer、Follow-up、Approval 等能力必须准确显示，不允许伪实现。

### 4.2 Agent Definition 与 Box 模型

- Runtime 由用户在 OMP、Codex 与 Claude Code 中选择。
- 模型字段提供 Runtime 对应的建议列表，同时允许用户填写任意模型 ID。
- 新 Box 固定引用创建时的 Agent Version，并默认使用该版本的模型；Box 可保存独立的 `model_override`，只影响该 Box。清空覆盖值后恢复 pinned Agent Version 模型；两者都为空时使用 Runtime CLI 默认模型。
- System Prompt 是可选的 Agent 持久指令；留空时使用 OMP/Codex/Claude CLI 自身默认 System Prompt。
- System Prompt 不允许保存账号、API Key 或其他 Secret；单次工作通过 Agent Terminal 输入。
- Agent Definition 更新产生新版本，不修改已经固定版本的 Box。

### 4.3 Runtime 权限默认值

- 原生 Agent Terminal 和结构化 Adapter 默认跳过 Runtime 自身的交互式权限提示：OMP 使用 `--approval-mode yolo`，Codex 使用 `--dangerously-bypass-approvals-and-sandbox`（App Server 对应 `approvalPolicy=never`、`sandbox=danger-full-access`），Claude Code 使用 `--permission-mode bypassPermissions --allow-dangerously-skip-permissions`。
- Control Plane 的 Approval Engine、审批记录和轮询仍保留，供显式启用审批的后台流程使用；默认 Runtime 启动策略不会依赖独立 Approval Web 页面。
- 示例 Server 与 daemon 配置默认启用 Claude Code；Host 缺少 Claude CLI 或认证时，Capability Probe 必须明确报告不可用。

## 5. Runtime 账号与凭证

- 第一版不管理 OMP/模型账号、API Key、OAuth Token 或 Provider Credential。
- Runtime 直接使用 Execution Host 当前系统用户已经配置好的认证信息。
- Control Plane 不收集、不同步、不展示 Runtime API Key。
- Agent Definition 只保存模型名、Prompt、Tools/Skills Policy 等非凭证配置。
- Host 侧 Credential 缺失时，Runtime 启动失败必须明确返回错误，不允许静默降级到其他账号。

## 6. 配置管理

禁止把部署配置、地址、Token、路径、并发、TTL、Runtime 开关硬编码到业务代码。

### 6.1 agentbox-server

- 默认配置路径：`~/.agentbox/server.json`。
- 可通过 CLI `--config` 或环境变量仅覆盖“配置文件路径”。
- 数据库地址、HTTP/gRPC 地址、Enrollment Token、Web 目录、Reaper 周期、Webhook Secret、Runtime Feature Flag 均来自配置文件。

### 6.2 agentboxd

- 配置根目录：`~/.agentboxd/`。
- 默认配置文件：`~/.agentboxd/config.json`。
- 默认状态目录：`~/.agentboxd/state/`。
- Credential、Journal、Runtime Session、Prompt Snapshot 均存放在该目录的受限子目录中。
- 可通过 CLI `--config` 或环境变量仅覆盖“配置文件路径”。
- Server 地址、Runtime Binary、并发限制、超时和 Runtime Feature Flag 来自配置文件；项目目录由用户在 Web Console 注册，不要求编辑 daemon 配置。
- 首次启动时 daemon 在本地配置文件原子生成 `hostId` UUID；后续启动永久复用。`hostName` 是用户自定义展示名，`systemHostname` 每次从操作系统读取并同步到 Server。

### 6.3 权限

- 配置目录权限应为 `0700`。
- 含 Enrollment Token/Host Credential 的文件权限应为 `0600`。
- 配置加载时拒绝未知字段和不合法值。

## 7. Execution Host 平台

`agentboxd` 第一版必须支持：

- macOS arm64/amd64。
- Linux arm64/amd64。

平台要求：

- macOS 提供 LaunchAgent 配置和安装脚本。
- Linux 提供 systemd Unit 和安装脚本。
- Agent Terminal 依赖 tmux；安装脚本必须检测 tmux 并给出 macOS/Linux 安装指令。
- 进程树停止、Workspace 路径校验、文件权限、日志路径在两个平台上行为一致。
- 平台特有能力通过构建标签或小型 Adapter 隔离，禁止在核心业务逻辑中散布 OS 分支。

## 8. 多用户与权限

- Web Console 始终在线并支持多人同时访问。
- Organization 账号角色只保留 `admin` 与 `user`：Admin 管理账号、Agent Definition、Host/Workspace 和组织级资源；User 使用已授权的 Workspace、Box、Schedule、Approval 与 Terminal。
- 首次启动自动创建默认管理员 `admin/admin123`，密码只保存为 bcrypt hash，并强制首次登录修改；后续启动不得覆盖管理员已修改的密码。
- Admin 可以创建、查询、修改和软删除账号，切换 `admin/user` 角色、启用/禁用账号和重置临时密码；系统必须阻止管理员禁用、删除或降级自己，并保证至少一个活跃 Admin。
- 支持 Workspace ACL 和 Box ACL，资源角色继续使用 Owner/Operator/Viewer，与账号角色分层。
- 所有登录、密码修改、账号创建/修改/禁用/重置、Prompt、Approval、Stop、ACL 和角色变更必须进入 Audit Log。

## 9. Box、Session 与交互入口

- Agent Definition、Agent Box、Execution Host 分离；Agent Box 本质是绑定 Agent、Host、项目目录的长期工作 Session。
- 每个 Box 提供两个交互入口：Agent Terminal 与 Command Terminal；Box 列表和所有 Box 深链默认进入 Agent Terminal。
- Agent Terminal 是默认入口，使用 xterm.js → WebSocket → gRPC → PTY → tmux attach → 原生 OMP/Codex TUI。
- Agent Terminal 中用户发送 Prompt 后可以关闭网页；WebSocket 断开不得停止 Agent、当前任务、Tool 或子进程。
- 每个 Box 使用固定 tmux Session `abox-agent-<box-id>`；重新进入时 attach 同一 Session。
- Subagent、Todo、Artifact、Diff、Notification 与 Schedule 保留独立结构化页面；Approval Engine 仅作为后台能力，不提供独立审批页面。
- Command Terminal 是临时项目 Shell，用于 Git、测试、日志和排障，页面离开后可以关闭。
- 创建 Agent Definition 不启动进程；Box 第一次进入 Agent Terminal 时按需启动 Runtime/tmux Session。
- 单 Box 同一时间最多一个 Main Run；Prompt、Follow-up、Steer 保持原有语义。
- Runtime 空闲后可以 Hibernation；下一 Prompt 自动 Resume。
- 删除 Box 会终止 Runtime、kill 对应 tmux Agent Terminal、从默认列表移除 Session，并保留 Audit/短期恢复数据。
- Box 创建者默认拥有 Private Box；仅创建者和 Admin 可切换为 Organization Visible，Admin 始终可查看全部 Box。
- Agent Definition、未被活跃 Box/Schedule 引用的 Agent、未被活跃 Box 使用的 Workspace、无资源依赖的 Offline Host 均支持受保护的软删除。

## 10. 移动端与呈现上下文

Agent 必须感知发起 Prompt 的 Client 呈现环境，从而调整回答排版。

Web 在每次发送 Prompt/Steer/Follow-up 时提交 Presentation Context：

```json
{
  "viewportWidth": 390,
  "viewportHeight": 844,
  "deviceClass": "mobile",
  "orientation": "portrait",
  "touch": true,
  "locale": "zh-CN",
  "timezone": "Asia/Shanghai",
  "prefersReducedMotion": false,
  "surface": "conversation"
}
```

要求：

- Presentation Context 与 Message 一起持久化。
- Server 将该 Context 传给 Host，daemon 以隐藏上下文附加到该 Turn 的 Runtime Input，不修改用户看到的原始 Message。
- 移动端默认要求短段落、避免宽表格、避免超长代码行、优先分点和可折叠结构。
- 桌面端可以使用更宽表格、并排内容和更完整代码块。
- 同一个 Box 的不同用户/设备各自使用本次 Prompt 携带的 Context，不能用全局 Box 级窗口宽度覆盖其他用户。
- 页面仅 Resize、没有新 Prompt 时，UI 自身响应式重排；第一版不使用 Steer 打断正在执行的 Agent。

### 10.1 中文用户体验

- Web Console 默认根据浏览器语言选择中文或英文；中文浏览器默认使用简体中文。
- 提供显式语言切换，并在浏览器中持久化用户选择。
- 中文文案应自然、简洁，避免生硬逐词翻译和不必要的英文缩写。
- Runtime、模型、Tool、命令、代码、路径和日志原文保持不翻译；配套解释使用中文。
- API 返回稳定错误码，Web 按语言映射用户可读错误；后端英文错误不直接暴露为主要提示。
- 时间、时区、数字、相对时间、状态、角色、审批风险和通知使用 `Intl` 按当前 locale 格式化。
- Presentation Context 携带 locale；中文用户发起的 Agent Turn 默认要求使用中文回答，除非用户明确指定其他语言。
- 移动端中文排版优先短段落、分点和适当留白，避免宽表格与横向滚动。

## 11. Automation 与工作结果

第一版应支持：

- Cron Schedule。
- Signed Webhook Trigger。
- Run History。
- In-app Notification。
- Remote Approval 与 Approval Timeout。
- Subagent Tree。
- Todo。
- Artifact 列表/下载。
- Workspace Diff。
- Agent Terminal（tmux 持久原生 OMP/Codex TUI）。
- Command Terminal（临时项目 Shell）。
- Idle Hibernation 与 Resume。

## 12. 安全边界

- Control Plane 不直接执行用户 Shell。
- Runtime 和 Terminal 仅由 Host daemon 执行。
- Workspace 由 Web Console 注册；daemon 通过 `realpath` 验证目录存在、是目录、位于当前 Host 系统用户的 Home 目录内，并拒绝 daemon state 目录。
- Terminal 需要 Box Operator 权限。
- Host Command、Message、Event、Schedule Trigger 必须幂等。
- Runtime Event 先持久化再 SSE 广播。
- 第一版不管理模型 Secret，但仍需保护 Host Enrollment Credential 与 Control Plane Webhook Secret。
- Agent Terminal 的 WebSocket Close 只能 detach tmux Client；只有删除 Box 或显式结束 Session 才能 `tmux kill-session`。

## 13. 验收标准

### 13.1 组件与部署

- [x] Control Plane Docker 镜像采用多阶段构建和 non-root distroless Runtime。
- [x] Docker Compose 能启动 PostgreSQL 与 Server/Web Control Plane，原生 daemon 可通过映射的 gRPC 端口注册。
- [x] 仅在 merge 到 `main` 后运行 Security、CodeQL、Secret Scan、Trivy 和 Go CI/golangci-lint；不阻塞 PR 开发流程。
- [x] Web、Server、daemon 独立构建。
- [x] Server 可嵌入 Web 静态资源单点部署。
- [x] Server 配置完全来自配置文件。
- [x] daemon 默认读取 `~/.agentboxd/config.json`。
- [ ] macOS LaunchAgent 可安装、启动、停止、卸载。
- [ ] Linux systemd Service 可安装、启动、停止、卸载。

### 13.2 Runtime 与 Agent Definition

- [x] 默认显示和启动 OMP、Codex 与 Claude Code。
- [x] Host 未安装或未登录对应 Runtime 时明确显示不可用，不能创建该 Runtime 的 Box。
- [x] OMP 使用 Host 当前系统认证完成真实调用。
- [x] Codex 使用 Host 当前系统认证完成真实调用并保持多轮 Thread。
- [x] Claude Code 使用 Host 当前系统认证完成真实调用。
- [x] Claude Host 同时支持 OAuth/Keychain 登录和 daemon 环境中的 `ANTHROPIC_API_KEY`；认证成功但交互 Onboarding 未完成时，由 daemon 备份并原子补齐标记，避免每台 Host 重复登录。
- [x] OMP 的 Prompt、Steer、Follow-up、Interrupt、Stop、Resume、Subagent、Todo 与 Tool Event 实测通过。
- [x] Approval Reaper 和后台 Approval 状态机保留，但 Web 不提供独立审批页面。
- [x] Agent Definition 模型作为新 Box 默认值；Box 可单独切换模型并安全重启 Runtime/tmux。
- [x] 新建 Agent 暂停 System Prompt 输入；Claude 默认模型为 `claude-opus-4-6`，OMP/Codex 默认模型为 `gpt-5.6-sol`，模型字段仍可修改。
- [x] System Prompt 可留空并使用 Runtime CLI 默认值；模型支持建议列表、自由填写和 Runtime 默认值。

### 13.3 Remote 与多用户

- [ ] 手机通过 Tailnet 打开 Web Console 并操作远端 Host Agent。
- [x] 页面断开后 Agent 继续执行。
- [x] 重连后通过 Event Seq 完整回放。
- [x] 默认 `admin/admin123` 可首次登录，并被强制修改临时密码。
- [x] Admin 可创建、启用/禁用账号、切换 Admin/User 角色并重置临时密码。
- [x] User 调用账号管理、Agent Definition 创建和 Workspace 注册 API 返回 403，但可使用被授权的 Box、Schedule、Approval 与 Terminal。
- [x] 密码修改、密码重置和账号禁用会撤销旧 Session；禁止管理员锁定自己或移除最后一个活跃 Admin。
- [x] 多用户消息显示正确作者。
- [x] Admin 可修改 Legacy Identity 的角色，并可安全软删除账号；账号管理覆盖增删查改。
- [x] Private Box 仅 Owner/Admin 可见；Organization Visible Box 对普通 User 可见。

### 13.4 Presentation Context

- [x] 手机 Prompt 持久化 mobile Presentation Context。
- [x] daemon 注入隐藏 Presentation Context 到 OMP Turn。
- [x] Agent 在 mobile Context 下不输出宽表格并优先短段落。
- [x] 桌面 Prompt 使用 desktop Context。
- [x] 两个设备的 Context 不互相覆盖。
- [x] Resize 不产生新 Agent Turn、不打断当前 Run。

### 13.5 Automation

- [x] Schedule 能无人值守创建并完成 OMP Run。
- [x] Webhook 签名错误被拒绝，重复 Idempotency Key 不重复执行。
- [x] Approval Reaper 和 Hibernation Reaper 在 Server 重启后继续工作。
- [x] Artifact、Diff、Subagent、Todo、Notification API 与 UI 可用。
- [x] Admin 可删除未使用的 Agent Definition、Workspace 和 Offline Host；存在活跃依赖时返回 409。
- [x] Terminal 在授权 Workspace 中运行，不能逃逸 Host 用户 Home 安全边界。

### 13.6 可靠性与安全

- [x] Server 重启后 daemon 自动重连并恢复 Snapshot。
- [x] daemon 断网期间 Event 写入 Journal，重连后去重上传。
- [x] 重复 Command 不重复执行副作用。
- [x] 所有关键操作产生 Audit Log。
- [x] 配置、日志、Event 不包含 Runtime API Key。
- [x] PostgreSQL Migration 可在空库和已升级库执行。

环境验收待办：当前开发机未安装 Tailscale CLI，也没有 Linux/systemd 主机；因此 Tailnet 手机实机链路、macOS LaunchAgent 实际启停和 Linux systemd 实际启停保持未勾选。Release 构建、安装/卸载脚本、权限、LaunchAgent `plutil`、四种 OS/Arch 产物与校验和已通过本地验收。
### 13.7 Agent Terminal 与 Host Identity

- [x] daemon 首次启动生成本地 `hostId` UUID 并写入 `0600` 配置文件，重启后复用同一 UUID。
- [x] daemon 注册自定义 `hostName` 与系统 `systemHostname`，Web 可区分多台 Execution Host。
- [x] Agent Terminal 使用 Box 项目目录启动原生 OMP TUI。
- [x] 浏览器关闭后 tmux Session 和后台命令继续运行。
- [x] 新 WebSocket 能重新 attach 同一 tmux Session。
- [x] daemon 重启后仍能 attach Host 上存活的 tmux Session。
- [x] Agent Terminal、Command Terminal、Subagent、Todo、Artifact 和 Diff 在 Web 中有独立入口；不存在重复的结构化聊天 Console。
- [x] Command Terminal 保持临时 PTY 语义。
- [x] 删除 Box 会从列表隐藏 Session，并终止 Managed Runtime 和 tmux Agent Terminal。
- [x] Agent Terminal 和 Command Terminal 支持浏览器页面内宽屏模式：覆盖应用侧栏和页面标题但不调用浏览器 Fullscreen API，可通过工具栏按钮或 `Esc` 退出，并自动重新计算 PTY 行列数。
- [x] Admin 新建 Box 时填写 Host 本地路径；若该 Host 尚未注册此路径，Web 自动创建并等待 Workspace 验证成功，再使用新 Workspace 创建 Box；已注册路径直接复用。
- [x] Claude Box 可选择新会话或手动填写 Claude Session UUID 恢复历史对话；允许多个 Box 使用相同 Session Ref，Box 删除不删除 Claude 历史。
- [x] Box 列表默认按当前用户过滤，并支持切换到其他 Organization 用户或全部 Owner。
- [x] Host 可注册任意数量 Box；`maxActiveBoxes` 仅作为同时运行 Runtime 的并发上限。
- [x] Offline Host 删除可显式选择级联依赖：终止 Box、删除 Schedule、归档 Workspace、取消相关运行后吊销 Host Credential。
- [ ] 真实手机/Tailnet 网络切换后重新 attach 同一 tmux Session。

### 13.8 2026-09-18 UI 删除与创建回归待办

- [x] **Box 列表删除**：Box Owner 或 Admin 在每张有权限的 Box 卡片上无需滚动即可看到 `Delete <Box name>`；点击后显示确认弹窗，确认成功后 Box 从列表消失，后端依赖冲突以弹窗内联错误保留并可读；无权限用户不显示删除操作。
- [x] **Box 批量删除**：Box Owner 或 Admin 可多选有删除权限的 Box，支持全选当前筛选结果并在一次确认中批量删除；成功项立即从列表移除，失败项保持选中并逐项显示错误；无权限 Box 不提供选择框。
- [x] **Host 卡片删除**：Admin 在每张 Host 卡片上无需滚动即可看到 `Delete <Host name>`；Offline Host 可打开确认弹窗并删除，无资源依赖时成功、存在依赖时将 409 冲突显示为弹窗内联错误；Online Host 的删除控件仍保持可见但不可执行，并明确提示必须先离线；非 Admin 不显示删除操作。
- [x] **Agent Definition 删除**：Admin 在每条 Agent Definition 上无需滚动即可看到 `Delete <Agent name>` 并通过确认弹窗删除未被活跃 Box 或 Schedule 引用的定义；受保护引用返回的冲突显示为弹窗内联错误；非 Admin 不显示删除操作。
- [x] **Workspace 删除**：Admin 在每条 Workspace 上无需滚动即可看到 `Delete <Workspace name>` 并通过确认弹窗删除未被活跃 Box 使用的 Workspace；受保护引用返回的冲突显示为弹窗内联错误；非 Admin 不显示删除操作。
- [x] **一步创建 Agent Box**：创建页在同一表单中同时提供名称、Agent、目标 Host、项目目录/Workspace，并且仅有一次提交动作；不存在分步器，成功提交后创建完整绑定的 Box。
- [x] **PWA 更新激活**：新 Web 构建发布后，已打开或再次访问的 PWA 客户端自动激活等待中的 Service Worker，并自动重新加载到最新构建，无需用户手动清缓存或关闭全部标签页。
- [x] **Runtime 可用性恢复**：Server Frame Idempotency Store 采用有界滚动保留，不因长期心跳耗尽容量；Control Plane 重启后 daemon 通过 gRPC keepalive 和主动重连恢复连接；Agent 创建弹窗打开期间自动刷新 Host Runtime 状态，在线 Host 的 OMP、Codex、Claude 在 E2E 等待预算内重新显示为可用。
- [x] **浏览器 E2E 回归覆盖**：真实 Chrome 覆盖 Box 单项/批量删除、Owner 默认/用户筛选、Claude 手动 Resume 重复绑定、Host 级联删除、Agent 默认模型、System Prompt 入口关闭、未注册路径自动 Workspace、Terminal 页面内宽屏、Runtime 可用性与 PWA 更新；隔离 Fixture Suite `npm --workspace apps/web run test:e2e` 共 16 项通过，真实本机部署 Suite `npm --workspace apps/web run test:e2e:local` 共 4 项通过。


## 14. 范围外

第一版不包含：

- Runtime Policy 可配置化和细粒度 Tool/Skill 强制执行。
- 模型账号/API Key/OAuth 管理。
- 公网 Relay。
- Kubernetes/VM/UTM 生命周期管理。
- 多 Control Plane 实例和跨地域高可用。
- 原生 iOS/Android App。
