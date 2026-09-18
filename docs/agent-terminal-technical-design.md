# Agent Terminal 技术设计

- 状态：Draft
- 版本：V1 设计基线
- 关联需求：[`product-requirements.md`](./product-requirements.md)
- 关联架构：[`agent-box-technical-design.md`](./agent-box-technical-design.md)

## 1. 目标

Agent Terminal 让用户通过 Web 或移动端远程操作 Execution Host 上的原生 OMP/Codex TUI，交互体验尽量接近本地 iTerm2：

- 使用 Box 绑定的本地项目目录作为 `cwd`。
- 完整传输 ANSI、光标、颜色、鼠标、IME、快捷键和全屏 TUI。
- 浏览器刷新、切后台或网络断开后可恢复或重新连接。
- 支持 macOS/Linux、arm64/amd64 Host。
- Agent Terminal 不解释 OMP/Codex 输出协议，只转发终端字节。
- **硬性不变量**：用户发送 Prompt 后可以立即关闭网页；浏览器和 WebSocket 断开不得停止 OMP/Codex、当前任务或子进程，任务必须在 Host 后台继续运行。

## 2. 术语与入口

每个 Box 提供两个交互入口：

| 入口 | 用途 | 后端 |
|---|---|---|
| Agent Terminal | 原生 OMP/Codex TUI，面向用户持续 Prompt | PTY/tmux/native resume |
| Command Terminal | 临时 Shell，用于 Git、测试、日志和排障 | 直接 PTY + 登录 Shell |

结构化 Runtime 协议继续服务 Schedule、Approval、Audit 和 Projection，但不再提供独立聊天 UI，也不尝试把终端 ANSI 输出转换为结构化消息。

## 3. 公共架构

```mermaid
flowchart LR
    Client[Web / Mobile xterm.js]
    WS[agentbox-server WebSocket]
    Hub[Terminal Hub]
    GRPC[Host gRPC Stream]
    Daemon[agentboxd Terminal Manager]
    Backend[Terminal Backend]
    Runtime[OMP / Codex TUI]

    Client <-->|input / ANSI / resize| WS
    WS <--> Hub
    Hub <-->|TerminalInput / TerminalData| GRPC
    GRPC <--> Daemon
    Daemon <--> Backend
    Backend <--> Runtime
```

公共安全约束：

- WebSocket 必须先通过 User、Organization、Box ACL 校验。
- Agent Terminal 需要 Box Owner/Operator 权限。
- Box ID、Host ID、Workspace ID、Runtime Type 都由 Server 从数据库读取；浏览器不能提交权威值。
- Workspace 由 daemon 执行 `realpath`，必须位于 Host 系统用户 Home 内，且不能位于 `~/.agentboxd/state`。
- Runtime 凭证沿用 Host 系统用户现有 OMP/Codex 配置，不经过 Control Plane。
- Terminal 数据不写入普通应用日志。
- 每条连接和 Session 生命周期写入 Audit Log，但不记录终端正文。

## 4. 方案一：直接 PTY

### 4.1 链路

```text
xterm.js → WebSocket → gRPC → agentboxd → PTY → OMP/Codex
```

agentboxd 直接创建子进程：

```bash
cd <workspace>
omp
# 或
codex
```

### 4.2 生命周期

浏览器连接拥有 PTY。连接断开时关闭 PTY，并终止对应 CLI 进程。

### 4.3 优点

- 最少依赖。
- 交互是真实原生 TUI。
- 实现、排障和资源回收简单。
- 适合作为 Command Terminal 的既有实现。

### 4.4 缺点

- 浏览器断开会中断当前任务。
- 无法满足远程持续 Prompt 的核心需求。
- daemon 重启、Server 重启或网络切换都会丢失活进程。

### 4.5 定位

Agent Terminal 可保留 `direct_pty` 作为诊断/兼容 Backend，但不能作为默认方案。

## 5. 方案二：原生 Session Resume

### 5.1 链路

```text
connect → PTY → omp --resume <session-ref>
connect → PTY → codex resume <thread-id>
```

### 5.2 Session 映射

Server 按 Box ID 查询最新非空 Session Ref：

```sql
SELECT session_ref
FROM runtime_instances
WHERE box_id = $1
  AND session_ref IS NOT NULL
ORDER BY started_at DESC, id DESC
LIMIT 1;
```

含义：

- OMP：Session 文件路径或 Session ID。
- Codex：Thread ID。

浏览器不能选择或覆盖 Session Ref。

### 5.3 生命周期

浏览器断开后 CLI 进程退出；已落盘的 Agent 上下文继续存在。重连时创建新 PTY，并调用 Runtime 原生 Resume。

### 5.4 优点

- 不依赖 tmux。
- Agent 历史可跨机器重启恢复。
- 每次连接都是干净进程。
- 可作为 tmux Session 丢失后的恢复机制。

### 5.5 缺点

- 断线时正在执行的 Turn、Tool 或命令可能被中断。
- 只恢复 Agent 上下文，不恢复 Shell、环境、当前 TUI 屏幕或其他子进程。
- OMP/Codex Resume 参数不同，需要 Runtime-specific Launcher。

### 5.6 定位

`native_resume` 作为无 tmux Host 的降级方案和灾难恢复方案。

## 6. 方案三：PTY + tmux attach

### 6.1 链路

```text
xterm.js → WebSocket → gRPC → PTY → tmux attach → OMP/Codex
```

每个 Box 使用固定 Session 名：

```text
abox-agent-<box-id>
```

首次打开：

```bash
tmux new-session -d \
  -s abox-agent-<box-id> \
  -c <workspace> \
  -- omp
```

Codex：

```bash
tmux new-session -d \
  -s abox-agent-<box-id> \
  -c <workspace> \
  -- codex
```

连接/重连：

```bash
tmux attach-session -t =abox-agent-<box-id>
```

### 6.2 生命周期

- tmux Server/Session 是 Agent Terminal 活进程的所有者。
- 每个 WebSocket 只拥有一个临时 `tmux attach` Client。
- 浏览器断开时关闭 attach Client，不执行 `tmux kill-session`。
- OMP/Codex、Tool、命令和 Pane 状态继续运行。
- 再次连接创建新 PTY，并 attach 相同 tmux Session。
- Box 删除时执行 `tmux kill-session`。
- daemon 重启不影响 tmux Server；机器重启会清空 tmux，需要 `native_resume` 兜底。

### 6.3 并发

V1 采用单写者：

- 第一个连接获得 Write Lease。
- 后续连接默认只读观察。
- Owner/Operator 可以显式抢占 Write Lease。
- 抢占和 Lease 变化进入 Audit Log。

不允许多个手机/浏览器同时向同一 TUI 写入键盘数据。

### 6.4 tmux 配置

Agent Box 创建 Session 后设置：

```bash
tmux set-option -t =<session> history-limit 50000
tmux set-option -t =<session> mouse on
```

Session 名只由规范化 Box UUID 生成，用户输入不能进入 tmux Target 参数。

### 6.5 优点

- 最接近 iTerm2。
- 当前任务在断线后继续。
- 原生 OMP/Codex TUI，无协议解析。
- daemon 重启后可重新 attach。
- tmux 内部 Scrollback、Copy Mode、Mouse 和 Pane 状态可用。

### 6.6 缺点

- Host 必须安装兼容版本 tmux。
- 机器重启后 tmux Session 不存在。
- Web 不了解 Pane/Window 语义。
- 多客户端写入必须额外仲裁。

### 6.7 定位

`tmux_attach` 是默认 Backend。

## 7. 方案四：tmux Control Mode

### 7.1 链路

```bash
tmux -C attach-session -t =abox-agent-<box-id>
```

agentboxd 解析 Control Protocol：

```text
%output
%window-add
%window-close
%pane-mode-changed
%session-changed
%layout-change
```

### 7.2 Web 映射

```text
Agent Box
├── Window 1
│   ├── Pane %1: Codex
│   └── Pane %2: npm run dev
└── Window 2
    └── Pane %3: logs
```

每个 Pane 可以映射成独立 xterm.js Surface。Web 可以创建 Split、切换 Pane、调整 Layout 和展示 Pane 通知。

### 7.3 优点

- 可实现类似 cmux 的多 Pane Workspace。
- Pane/Window 生命周期结构化。
- 适合 Agent Team 和并行任务。
- 能把目录、端口、进程和 Pane 元数据显示在侧栏。

### 7.4 缺点

- 需要完整维护 tmux Control Protocol 状态机。
- `%output` 解码、Pane Backpressure、Layout 同步和版本兼容成本高。
- 仍然不知道 OMP/Codex 内部 Tool/Todo 语义。
- 不如普通 attach 路径直接。

### 7.5 定位

`tmux_control` 作为实验性 Backend，用于后续多 Pane/多 Agent Workspace；不作为 V1 默认。

## 8. 方案五：Runtime 协议 Console

该方案不是原生终端：

```text
Web → HTTP/SSE → Server → gRPC → daemon → OMP RPC / Codex App Server
```

优点：

- Message、Tool、Todo、Subagent、Approval、Artifact 结构化。
- 支持 Schedule、Audit、多用户、Event Seq 回放。
- 不受终端宽高影响。

缺点：

- 无法提供 iTerm2/原生 TUI 体验。
- 与 Agent Terminal 是独立 Session。

定位：仅作为后台 Automation、Approval、Audit 和 Projection 通道，不提供独立 Web Console。

## 9. 外部组件方案

### 9.1 ttyd

[ttyd](https://github.com/tsl0922/ttyd) 提供成熟的 xterm.js、WebSocket、PTY、CJK/IME、WebGL、文件传输和鉴权代理能力。

不直接采用：

- 需要额外端口和进程。
- 重复 Agent Box 的身份、ACL、Host Stream 和 Workspace Guard。
- Terminal 生命周期难以纳入现有 Audit/Box 模型。

### 9.2 cmux

[cmux](https://github.com/manaflow-ai/cmux) 是 macOS Swift/AppKit + libghostty 应用，值得借鉴：

- Workspace/Surface 层级。
- 侧栏目录、Git、PR、端口和通知元数据。
- Attention Ring 与 Notification Center。
- Agent 原生 Session Resume。
- tmux 用于真正的 Live Process detach/reattach。

不直接复用：

- macOS 原生实现，不是 Web 组件。
- libghostty 不能直接作为浏览器渲染器。
- GPL-3.0-or-later 代码存在许可边界。

## 10. Backend 配置

Box 保存：

```json
{
  "agentTerminalBackend": "tmux_attach"
}
```

枚举：

```text
direct_pty
native_resume
tmux_attach
tmux_control
```

最终数据库 CHECK：

```sql
CHECK (agent_terminal_backend IN (
  'direct_pty',
  'native_resume',
  'tmux_attach',
  'tmux_control'
))
```

默认：`tmux_attach`。

生产选择器 V1 只开放 `tmux_attach`。`direct_pty` 仅用于诊断，`native_resume` 仅用于 tmux 丢失或机器重启后的恢复；两者都不满足“断线后当前任务继续”的硬性要求。`tmux_control` 在完成兼容矩阵前保持实验状态。

Backend 只能由 Admin 在创建 Box 时选择；修改 Backend 需要终止现有 Agent Terminal Session，并写入 Audit Log。

## 11. Web 路由与命名

```text
/boxes/:id/agent-terminal   Agent Terminal
/boxes/:id/command-terminal Command Terminal
/boxes/:id/subagents        Subagents
/boxes/:id/todos            Todos
/boxes/:id/artifacts        Artifacts
/boxes/:id/diff             Diff
```

Box 列表、Approval、Notification 和 Schedule 深链默认进入 Agent Terminal。Box 顶部展示 Agent Terminal、Command Terminal 及四个结构化输出页面。

## 12. Terminal Transport

WebSocket Client Message：

```json
{"type":"input","data":"..."}
{"type":"resize","columns":120,"rows":32}
{"type":"close"}
```

Host `TerminalInput`：

```text
session_id
box_id
workspace
mode: agent | command
backend: direct_pty | native_resume | tmux_attach | tmux_control
runtime_type: omp | codex
data
columns
rows
open
close
terminate
```

Terminal transport 不携带 Runtime API Key，也不允许浏览器提交 SessionRef。需要 SessionRef 的 `native_resume` Backend 由 Server 从数据库读取。

## 13. 移动端体验

- 输入区域必须支持中文 IME。
- 提供 Ctrl、Alt、Esc、Tab、Ctrl-C、Ctrl-B 和方向键工具栏。
- 支持安全区、软键盘 Resize 和横竖屏切换。
- 默认隐藏复杂 Chrome，只保留 Terminal、连接状态和 Session 名。
- 网络切换后指数退避重连，并 attach 原 Session。
- 单写者丢失 Lease 后输入框进入只读，明确提示当前写入者。

## 14. 可观测性

Metrics：

```text
agentboxd_terminal_connections{mode,backend}
agentboxd_terminal_sessions{backend}
agentboxd_terminal_bytes_total{direction}
agentboxd_terminal_reconnects_total{backend}
agentboxd_terminal_backend_errors_total{backend,reason}
```

禁止 Box ID、User ID、Session 名作为 Metric Label。

Diagnostics：

- tmux binary/version。
- Backend 可用性。
- 活跃 attach Client 数。
- tmux Agent Session 数。
- 最近一次固定分类错误，不包含 Terminal 正文。

## 15. 安全与资源限制

- 单连接输入消息大小上限。
- PTY 输出使用有界 Channel；慢客户端断开，不阻塞 tmux/Runtime。
- Terminal WebSocket 有 Ping/Pong 和 Idle Detection。
- 最大 Terminal Client 数配置化。
- tmux Session 名从 Box UUID 派生，不拼接用户输入。
- Command Terminal 断开即杀 PTY；Agent Terminal 按 Backend 生命周期处理。
- 删除 Box 必须终止所有 attach Client，并清理对应 tmux Session。
- Audit 仅记录连接、断开、抢占、Backend 切换和删除，不记录输入输出正文。

## 16. 实施顺序

### Phase A：公共命名与路由

- 当前 Terminal 改名 Command Terminal。
- 新增 Agent Terminal 路由。
- Box 默认链接进入 Agent Terminal。
- 配置 `agent_terminal_backend`。

### Phase B：`direct_pty` 诊断模式

- 复用现有 PTY 实现。
- 浏览器断开时终止进程。
- 仅用于兼容性诊断，不进入生产 Backend 选择器。

### Phase C：`native_resume` 灾难恢复

- 只从 Server 权威数据库读取 SessionRef。
- OMP/Codex 分别调用原生 Resume。
- 验证断线时当前任务中断、历史可恢复。
- 仅用于 tmux 丢失或机器重启后的恢复，不进入生产 Backend 选择器。

### Phase D：`tmux_attach`

- 首次创建 tmux Session。
- 连接仅 attach/detach。
- Box 删除时 kill-session。
- 单写者 Lease。
- 作为默认 Backend。

### Phase E：`tmux_control`

- 实现实验性 Control Client。
- 输出 Pane/Window Snapshot。
- 支持 Pane 创建、关闭、聚焦和 Resize。
- 在完成版本兼容矩阵前不设为默认。

## 17. 验收矩阵

| 场景 | direct_pty | native_resume | tmux_attach | tmux_control |
|---|---:|---:|---:|---:|
| OMP 原生 TUI | 必须 | 必须 | 必须 | 必须 |
| Codex 原生 TUI | 必须 | 必须 | 必须 | 必须 |
| 项目目录 cwd | 必须 | 必须 | 必须 | 必须 |
| 中文 IME | 必须 | 必须 | 必须 | 必须 |
| 浏览器刷新 | 新进程 | 恢复上下文 | 同一活进程 | 同一活进程 |
| 网络断开任务继续 | 否 | 否 | 是 | 是 |
| daemon 重启后 attach | 否 | 新进程恢复 | 是 | 是 |
| 机器重启后恢复 | 否 | 是 | native resume 兜底 | native resume 兜底 |
| 多 Pane | 否 | 否 | tmux 内部可用 | Web 可管理 |
| Box 删除清理 | PTY kill | PTY kill | kill-session | kill-session |
| 单写者仲裁 | 必须 | 必须 | 必须 | 必须 |

## 18. 推荐结论

V1 生产默认且唯一可选：

```text
tmux_attach
```

灾难恢复：

```text
native_resume
```

诊断模式：

```text
direct_pty
```

实验性高级模式 `tmux_control` 在完成版本兼容矩阵和断线续跑验收前不得开放给生产用户。

Agent Terminal 始终是协议无关的 Terminal Transport；OMP RPC/Codex App Server 只作为后台结构化自动化通道，不形成第二套用户交互入口。

## 19. Architecture Decision Record：Agent Terminal Backend 选型

- 日期：2026-09-17
- 决策状态：Accepted
- 决策范围：Agent Terminal 的 Host 端 Session Owner 与 Web 断线语义

### 19.1 用户核心场景

用户无法持续坐在家中电脑前，需要在外部通过手机或网页继续操作家中项目目录里的原生 OMP/Codex Terminal：

```text
发送 Prompt
→ Agent 开始长时间执行
→ 用户关闭网页/手机切后台/网络切换
→ Agent、Tool 和子进程继续在 Host 后台运行
→ 用户稍后重新进入
→ 回到同一个活 Terminal Session，查看结果并继续 Prompt
```

### 19.2 硬性门槛

参与 PK 的方案必须尽量满足：

1. 原生 OMP/Codex TUI，体验接近 iTerm2。
2. 浏览器和 WebSocket 断开后当前任务继续运行。
3. 重连后回到同一个活进程和同一个 Terminal Screen。
4. Terminal Transport 不理解 OMP/Codex 输出协议，只透明转发字节。
5. 使用 Box 绑定的项目目录。
6. 支持 Web 和手机端 xterm.js。
7. daemon 重启后可重新连接 Host 上仍存活的 Session。
8. Box 删除时能确定性终止 Session。
9. 不引入独立的第二套鉴权、端口和 Workspace 安全边界。

### 19.3 方案 PK

| 方案 | 断线任务继续 | 同一活进程 | 原生 TUI | 协议无关 | daemon 重启后恢复 | 工程成本 | 结论 |
|---|---:|---:|---:|---:|---:|---:|---|
| Direct PTY | 否 | 否 | 是 | 是 | 否 | 低 | 淘汰为 Agent Terminal；保留给 Command Terminal |
| Native Resume | 否 | 否 | 是 | 需要 Runtime-specific SessionRef | 新进程恢复 | 中 | 淘汰为默认；保留作灾难恢复 |
| PTY + tmux attach | 是 | 是 | 是 | 是 | 是 | 中 | **V1 选中** |
| tmux Control Mode | 是 | 是 | 需要额外 Web 映射 | 是 | 是 | 高 | 延后到多 Pane 阶段 |
| Runtime Protocol Console | 是 | 是 | 否 | 否 | 是 | 高 | 仅保留后台协议能力，不提供 Web Console |
| ttyd 直连 | 否 | 否 | 是 | 是 | 否 | 中 | 淘汰 |
| ttyd + tmux | 是 | 是 | 是 | 是 | 是 | 中 | 功能满足，但重复现有 Server/daemon/Auth/ACL，淘汰 |
| cmux | 是 | 是 | 是 | 是 | 是 | — | 作为产品与 Session Restore 参考，不可直接复用为 Web Backend |

### 19.4 淘汰原因

#### Direct PTY

WebSocket 生命周期拥有 PTY；页面关闭即终止 Agent，直接违反“Prompt 后可以关网页”的硬性需求。

#### Native Resume

恢复的是落盘上下文，不是原活进程。断线期间当前 Turn、Tool、测试或子进程可能被中断。只适合机器重启后恢复。

#### tmux Control Mode

功能上满足，但 V1 只需要一个 Box 一个 Terminal。解析 `%output`、Pane/Window/Layout 状态和不同 tmux 版本兼容属于过度设计。未来多 Agent/多 Pane Workspace 再启用。

#### Runtime Protocol Console

结构化能力强，但不是原生 Terminal；仅保留其后台 Schedule、Approval、Audit 与 Projection 能力。

#### ttyd/WeTTY/GoTTY

会引入额外进程、端口、WebSocket、鉴权和 PTY 生命周期，重复 Agent Box 已有能力，并扩大攻击面。

### 19.5 最终决策

V1 Agent Terminal 采用：

```text
xterm.js
→ authenticated WebSocket
→ agentbox-server Terminal Hub
→ existing Host gRPC stream
→ agentboxd PTY client
→ tmux attach
→ native OMP/Codex TUI
```

每个 Box 对应固定 tmux Session：

```text
abox-<box-id>
```

第一次进入创建 tmux Session；后续连接只 attach。WebSocket Close 的语义是 detach，不是 terminate。只有 Box 删除或显式结束 Agent Terminal 才执行 `tmux kill-session`。

### 19.6 产品入口结果

```text
Agent Terminal
  默认入口；tmux 持久原生 OMP/Codex TUI。

Command Terminal
  临时 PTY Shell；用于 Git、测试、日志和排障，离开即关闭。

Structured Output
  Subagent、Todo、Artifact、Diff、Approval 与 Schedule 使用独立只读或操作页面。
```

Box 列表和所有 Box 深链默认进入 Agent Terminal；产品不再维护重复的结构化聊天界面。

### 19.7 结果与已验证项

- tmux Session 名由 Box UUID 派生，不接受用户输入。
- OMP 原生 TUI 可以在项目目录内启动并完成连续 Prompt。
- WebSocket 关闭后 tmux Session 保持，`session_attached=0`，Agent 进程继续。
- 新 WebSocket 可重新 attach 同一个 tmux Session。
- Command Terminal 继续使用直接 PTY，并保持临时生命周期。
- macOS 本机验证使用 tmux 3.6a。

### 19.8 已知边界

- tmux 无法跨机器关机/重启保存活进程；未来使用 Native Resume 恢复 Agent 上下文。
- V1 只允许一个写入者；多用户观察和抢占需要 Terminal Lease。
- tmux Control Mode 与 Web 多 Pane UI 延后。
- Agent Terminal 不渲染结构化 Tool/Todo/Approval Event；这些信息分别进入 Todo、Subagent、Artifact、Approval 等专用页面。