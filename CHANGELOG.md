# Changelog

本文件记录 AgentBox 各正式版本的重要更新。

格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，版本号遵循 [Semantic Versioning](https://semver.org/lang/zh-CN/)。

## [Unreleased]

## [0.8.1] - 2026-09-23

### Fixed

- 移除 Agent Terminal Surface 的垂直边框，修复深色终端左侧持续可见的细线。
- 新建 Box 选择 Attach 运行中 Claude Session 时正确提交 Session UUID；daemon 通过 `claude agents --json` 将 UUID 解析为 Background Job ID 后执行 `claude attach`/`claude stop`，并排除不可 Attach 的普通交互会话。

## [0.8.0] - 2026-09-23

### Added

- Agent Terminal 新增 10–24px 字号调节并保存在浏览器本地；手机显示 44px 触控终端快捷键。

### Changed

- Agent Terminal 使用 `VisualViewport`、`100dvh`、Safe Area 和 `ResizeObserver` 适配手机软键盘、横竖屏、平板及桌面窗口变化，并持续同步 xterm/PTY 行列数。

## [0.7.1] - 2026-09-23

### Fixed

- Web Toast ID 不再直接依赖仅限 Secure Context 的 `crypto.randomUUID()`；远程 HTTP、旧版浏览器或缺少该 API 的环境使用 `crypto.getRandomValues`/本地回退生成 UUID。

## [0.7.0] - 2026-09-22

### Added

- 运行中的 Claude Background Session 可通过 `claude attach <id>` 被多个 Box 重复 Attach；每个 Box 保留独立 AgentBox tmux 和浏览器 Client。
- 新增持久化 Attachment 记录、共享输入策略状态、可见 Attachment 数量、Box Detach 和 Admin 全局 Stop。

### Changed

- 删除 Box 或执行 Detach 只移除当前 Attachment，不停止共享 Claude Session；全局 Stop 是单独的 Admin 操作，并将所有关联 Box 转换为可 Resume 状态。


## [0.6.0] - 2026-09-22

### Added

- Admin 新建 Claude Box 时可查询所选 Host 的本地 Claude Session，按名称、Workspace 或 Session ID 搜索并一键选择。
- daemon 只读取 Claude Session 最小元数据，合并 `~/.claude/projects` 历史与 `~/.claude/sessions` 运行状态；发现失败时保留手动 Session ID 回退。


## [0.5.0] - 2026-09-22


### Added

- 新增 `docs/deployment.md`，覆盖 Control Plane/Worker 拓扑、macOS LaunchAgent、Linux systemd user service、PostgreSQL、OMP/Codex/Claude 认证、升级、回滚、健康检查和卸载。
- Agent Terminal 和 Command Terminal 新增浏览器页面内宽屏模式；不调用 Fullscreen API，支持工具栏退出和 `Esc` 快捷退出。
- Claude Box 可手动填写 Session UUID，通过 `claude --resume` 恢复本机历史对话；允许多个 Box 使用相同 Session Ref。
- Box 列表默认只显示当前用户的 Box，并支持按 Organization 用户或全部 Owner 筛选。
- Offline Host 删除支持显式级联：终止依赖 Box、删除 Schedule、归档 Workspace 后吊销 Host。

### Changed

- 新建 Agent 暂停 System Prompt 输入；Claude 默认模型为 `claude-opus-4-6`，OMP/Codex 默认模型为 `gpt-5.6-sol`，模型仍可编辑。
- Host 不再限制可注册的 Box 数量；`maxActiveBoxes` 仅限制同时运行的 Runtime 数量。

## [0.4.3] - 2026-09-22

### Added

- Claude Host 探测同时支持已登录的 OAuth/Keychain 凭据和 daemon 环境中的 `ANTHROPIC_API_KEY`；已认证主机自动备份并完成 Claude 交互式 Onboarding 标记，无需逐台重复登录。

### Fixed

- Claude Agent Terminal 将 `ANTHROPIC_API_KEY` 加入 tmux `update-environment`，确保长期运行的 tmux Server 为新 Session 注入 daemon 当前的 API Key，而不在命令行、数据库或普通配置中暴露密钥值。

## [0.4.2] - 2026-09-22

### Added

- 新增统一的版本更新记录文件。
- Agent Box 列表支持权限安全的多选和批量删除；成功项立即移除，失败项保留选择并显示逐项错误。

### Fixed

- Server Frame Idempotency Store 改为有界滚动保留，达到上限时淘汰最旧 Frame ID，修复 daemon 长期运行约 3 天后因 Store 满而每 15 秒断线、导致 OMP/Codex/Claude 显示不可用的问题；同时增加 gRPC keepalive、主动重连唤醒、连接状态日志和 Agent 创建弹窗的 Runtime 可用性自动刷新。

## [0.4.1] - 2026-09-21

### Fixed

- 在 Agent Box 列表卡片首屏增加删除入口；Box Owner 和 Admin 可以直接打开受保护的删除确认流程。
- 将 Host 和 Agent Definition 删除入口移动到卡片右上角，避免按钮位于折叠区域或首屏之外。
- 修复 Workspace 操作列宽度和移动端布局，确保分享与删除按钮同时可见。
- Online Host 保留可见的删除入口，但禁用确认操作并明确提示必须先停止对应 `agentboxd`。
- 显式注册和激活 PWA Service Worker；新版本接管页面后自动刷新，避免客户端长期停留在旧前端资源。

### Changed

- 将两步 Agent Box 创建流程合并为单表单：名称、Agent、执行主机、项目目录或已注册 Workspace 在同一页面完成选择，并通过一次提交创建 Box。

### Tests

- 新增真实 Chrome E2E，覆盖 Box、Host、Agent Definition、Workspace 删除入口、权限、依赖冲突、Host online/offline 状态和一步创建 Box。
- 新增 PWA Service Worker 更新、接管和自动刷新 E2E。
- 新增针对本机真实部署和真实数据库的浏览器 E2E。

## [0.4.0] - 2026-09-18

### Added

- 新增 AgentBox 本地账号密码认证和 Admin/User RBAC。
- 新增账号创建、查询、角色修改、启用/禁用、密码重置、Session 撤销和受保护删除。
- 新增 Box 独立模型覆盖；切换模型时安全重启结构化 Runtime 和持久 Agent Terminal。
- 新增 Private/Organization Visible Box 可见性和 Owner/Admin 管理规则。
- 新增 Box、Agent Definition、Workspace、Offline Host 的受保护删除 API 与 UI。
- 默认开放 OMP、Codex 和 Claude Code 三种 Runtime。
- 新增 Claude Code 结构化 Runtime 与原生 Agent Terminal 启动支持。
- Agent Terminal 右上角显示完整持久化 `session_id`，支持一键复制。
- 新增 Docker Compose、Go CI、CodeQL、Secret Scan、依赖审计、Trivy 和 `govulncheck` 工作流。

### Changed

- Agent System Prompt 改为可选；留空时使用 Runtime CLI 默认 System Prompt。
- Agent Terminal 成为 Box 的默认交互入口；移除重复的结构化 Agent Console。
- 移除独立 Approval Web 页面和前端轮询；后端 Approval Engine、Schedule、Audit 和 Projection 保持运行。
- Runtime 默认使用原生免审批模式：OMP `yolo`、Codex `dangerously-bypass-approvals-and-sandbox`、Claude Code `bypassPermissions`。
- Legacy Organization 角色安全迁移为 Admin/User。

### Fixed

- 修复登录页暴露默认账号密码的问题。
- 修复旧 Organization Role CHECK 约束导致的升级 Migration 失败。
- 修复静态资源路径和符号链接越界、Codex/OMP allocation-size overflow 等 CodeQL High 告警。
- Agent Terminal 光标改为不闪烁，并在 PTY 数据写入后主动刷新终端视图。

### Security

- 增加最后一个活跃 Admin 保护、自锁保护、登录限速和配置文件权限检查。
- GitHub Actions Runner 固定为 `ubuntu-24.04`，降低运行环境漂移风险。

## [0.3.0] - 2026-09-17

### Added

- 新增由 tmux 持有的持久原生 Agent Terminal Session。
- 浏览器关闭仅 detach；Agent 和当前任务继续运行，重新打开页面可 attach 同一 Session。
- daemon 重启后可重新连接 Host 上仍存活的 tmux Session。
- 新增独立的 Command Terminal，用于 Git、测试、日志和故障排查。
- 新增稳定 Host UUID、自定义 Host 名称和系统 Hostname 上报。
- 新增 Agent Terminal 技术设计与后台续跑 E2E。

### Changed

- Box、Notification 和 Schedule 深链默认进入 Agent Terminal。
- 安装包增加 tmux 前置依赖检查。

## [0.2.0] - 2026-09-17

### Added

- 新增 Codex Runtime Adapter，支持 App Server 初始化、认证探测、多轮 Thread、事件规范化和错误分类。
- 新增 Codex Host Capability 探测和配置开关。
- 新增直接填写 Host 本地项目路径创建 Box 的流程。
- 新增 Workspace 路径校验和 Host Home 安全边界。
- Web 新增 Runtime 对应的 Agent、Host、Workspace 和 Terminal 创建体验。

### Changed

- Runtime、Server、daemon 和数据库模型扩展为同时支持 OMP 与 Codex。
- 补充 Codex 和直接项目目录相关 Migration、配置与技术设计。

## [0.1.0] - 2026-09-16

### Added

- 完成 AgentBox 第一版技术设计、ERD 和产品需求。
- 建立 Go Control Plane、`agentboxd` Host daemon、PostgreSQL Schema 和 Migration 体系。
- 支持 Organization、User、Team、Agent Definition、Host、Workspace、Box、Run、Message、Event、Approval、Schedule、Artifact、Diff、Subagent、Todo 和 Notification 核心模型。
- 实现 OMP Runtime Adapter、Prompt/Steer/Follow-up/Interrupt/Stop/Resume 和 Tool Event 投影。
- 实现多用户访问控制、Box/Workspace ACL 和审计日志。
- 实现 Schedule、Webhook、Approval/Hibernation Reaper、Retention 和无人值守执行。
- 初步接入 Claude Runtime。
- 提供 React Web Console、Docker 镜像、macOS LaunchAgent、Linux systemd、安装/卸载脚本和四平台 Release 构建。

### Security

- 配置文件要求 `0600`，状态目录要求 `0700`。
- Host Credential、幂等 Command、Event Journal/Ack 和 PostgreSQL 约束用于保护控制面与 daemon 通信。
- Runtime Secret 仅引用 Host 环境，不在 Control Plane 保存明文。

[Unreleased]: https://github.com/foolzzz/abox/compare/v0.8.1...HEAD
[0.8.1]: https://github.com/foolzzz/abox/compare/v0.8.0...v0.8.1
[0.8.0]: https://github.com/foolzzz/abox/compare/v0.7.1...v0.8.0
[0.7.1]: https://github.com/foolzzz/abox/compare/v0.7.0...v0.7.1
[0.7.0]: https://github.com/foolzzz/abox/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/foolzzz/abox/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/foolzzz/abox/compare/v0.4.3...v0.5.0
[0.4.3]: https://github.com/foolzzz/abox/compare/v0.4.2...v0.4.3
[0.4.2]: https://github.com/foolzzz/abox/compare/v0.4.1...v0.4.2
[0.4.1]: https://github.com/foolzzz/abox/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/foolzzz/abox/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/foolzzz/abox/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/foolzzz/abox/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/foolzzz/abox/tree/v0.1.0
