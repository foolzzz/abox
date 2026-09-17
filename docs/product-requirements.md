# Agent Box 第一版产品需求

- 状态：Active
- 产品版本：V1
- 关联技术设计：[`agent-box-technical-design.md`](./agent-box-technical-design.md)
- 关联 ERD：[`agent-box-erd.md`](./agent-box-erd.md)

## 1. 产品目标

用户可以通过 Tailscale 虚拟局域网，在手机或外部电脑的 Web Console 中持续访问运行在本机或服务器上的 Agent Box。Agent 在用户断开页面后继续工作，用户重新连接后可以恢复对话、查看事件并继续下达指令。

第一版围绕 OMP Agent 提供完整可用产品；Claude Code Adapter 保留在代码中，但默认关闭，作为下一期功能。

## 2. 第一版组件

必须以三个独立组件开发和测试：

1. `agentbox-web`：Web/PWA Console。
2. `agentbox-server`：始终在线、多用户 Control Plane。
3. `agentboxd`：运行在 macOS 或 Linux Execution Host 上的守护进程。

`agentbox-web` 与 `agentbox-server` 是独立开发组件；首版允许 Web 静态资源嵌入 Server 进行单点部署。

## 3. 网络与远程访问

- 所有 Client、Server 和 Host 位于同一个 Tailscale Tailnet。
- 第一版不实现公网 Relay，不要求公网 IP，不开放 OMP/Claude stdio Runtime 到网络。
- Web Client 仅访问 `agentbox-server`。
- `agentboxd` 主动连接 `agentbox-server`，Runtime 只在 Host 本地运行。
- 禁止依赖 Tailscale Funnel。

## 4. Runtime 范围

### 4.1 第一版

- 仅对用户开放 OMP Runtime。
- OMP 使用 `omp --mode rpc`。
- 支持多轮 Prompt、Steer、Follow-up、Interrupt、Stop、Resume、Subagent、Todo、Tool Event 和 Remote Approval。

### 4.2 下一期

- Claude Code stream-json Adapter 可以保留和继续测试。
- 默认配置 `enableClaude=false`。
- Server、daemon 和 Web 不得在第一版默认暴露 Claude 创建入口。
- 启用 Claude 必须通过明确配置，而不是代码修改。

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
- Server 地址、Workspace Roots、Runtime Binary、并发限制、超时和 Runtime Feature Flag 均来自配置文件。

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
- 进程树停止、Workspace 路径校验、文件权限、日志路径在两个平台上行为一致。
- 平台特有能力通过构建标签或小型 Adapter 隔离，禁止在核心业务逻辑中散布 OS 分支。

## 8. 多用户与权限

- Web Console 始终在线并支持多人同时访问。
- 第一个 Organization Member 是 Owner；后续未知用户默认 Viewer。
- 支持 Owner/Admin/Operator/Viewer。
- 支持 Workspace ACL 和 Box ACL，角色为 Owner/Operator/Viewer。
- Viewer 默认只读；Operator 可以操作已分享 Box；Admin/Owner 管理 Organization 资源。
- 所有 Prompt、Steer、Follow-up、Approval、Stop、ACL 和角色变更必须进入 Audit Log。

## 9. Box 与对话

- Agent Definition、Agent Box、Execution Host 分离。
- 创建 Agent Definition 不启动进程。
- Box 第一次 Prompt 时按需启动 Runtime。
- 单 Box 同一时间最多一个 Main Run。
- Prompt 在空闲 Box 上启动新 Run。
- Follow-up 在活跃 Run 后排队。
- Steer 介入当前 Run。
- 页面断开不停止 Runtime。
- Runtime 空闲后自动 Hibernation；下一 Prompt 自动 Resume。

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
- Interactive Terminal。
- Idle Hibernation 与 Resume。

## 12. 安全边界

- Control Plane 不直接执行用户 Shell。
- Runtime 和 Terminal 仅由 Host daemon 执行。
- Workspace 必须通过 realpath 校验并限制在配置的 Workspace Roots 内。
- Terminal 需要 Box Operator 权限。
- Host Command、Message、Event、Schedule Trigger 必须幂等。
- Runtime Event 先持久化再 SSE 广播。
- 第一版不管理模型 Secret，但仍需保护 Host Enrollment Credential 与 Control Plane Webhook Secret。

## 13. 验收标准

### 13.1 组件与部署

- [x] Web、Server、daemon 独立构建。
- [x] Server 可嵌入 Web 静态资源单点部署。
- [x] Server 配置完全来自配置文件。
- [x] daemon 默认读取 `~/.agentboxd/config.json`。
- [ ] macOS LaunchAgent 可安装、启动、停止、卸载。
- [ ] Linux systemd Service 可安装、启动、停止、卸载。

### 13.2 OMP Runtime

- [x] 默认只显示和启动 OMP。
- [x] 未显式开启 Feature Flag 时不能创建 Claude Box。
- [x] OMP 使用 Host 当前系统认证完成真实调用。
- [x] 连续两轮对话保持 Session。
- [x] Prompt、Steer、Follow-up、Interrupt、Stop、Resume 实测通过。
- [x] Subagent、Todo、Tool Event 可展示。
- [x] Remote Approval 可批准、拒绝和超时默认拒绝。

### 13.3 Remote 与多用户

- [ ] 手机通过 Tailnet 打开 Web Console 并操作远端 Host Agent。
- [x] 页面断开后 Agent 继续执行。
- [x] 重连后通过 Event Seq 完整回放。
- [x] 两个用户同时查看同一 Box。
- [x] Viewer 未授权操作返回 403。
- [x] Operator 可操作显式分享的 Box。
- [x] 多用户消息显示正确作者。

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
- [x] Terminal 在授权 Workspace 中运行，不能逃逸 Workspace Root。

### 13.6 可靠性与安全

- [x] Server 重启后 daemon 自动重连并恢复 Snapshot。
- [x] daemon 断网期间 Event 写入 Journal，重连后去重上传。
- [x] 重复 Command 不重复执行副作用。
- [x] 所有关键操作产生 Audit Log。
- [x] 配置、日志、Event 不包含 Runtime API Key。
- [x] PostgreSQL Migration 可在空库和已升级库执行。

环境验收待办：当前开发机未安装 Tailscale CLI，也没有 Linux/systemd 主机；因此 Tailnet 手机实机链路、macOS LaunchAgent 实际启停和 Linux systemd 实际启停保持未勾选。Release 构建、安装/卸载脚本、权限、LaunchAgent `plutil`、四种 OS/Arch 产物与校验和已通过本地验收。

## 14. 范围外

第一版不包含：

- Claude Code 对用户开放。
- 模型账号/API Key/OAuth 管理。
- 公网 Relay。
- Kubernetes/VM/UTM 生命周期管理。
- 多 Control Plane 实例和跨地域高可用。
- 原生 iOS/Android App。
