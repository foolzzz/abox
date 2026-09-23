# AgentBox 部署指南

本文说明如何使用正式 Release 在 macOS 或 Linux 上部署 AgentBox Control Plane 与 Host daemon。当前示例版本为 `v0.6.0`。

## 1. 部署拓扑

AgentBox 支持两种常见拓扑。

### 1.1 单机部署

同一台机器运行：

- PostgreSQL
- `agentbox-server`
- `agentboxd`
- OMP、Codex、Claude Code

适合个人使用、开发环境和单机工作站。

### 1.2 Control Plane + 多执行主机

Control Plane 运行：

- PostgreSQL
- `agentbox-server`
- Web Console

每台执行主机运行：

- `agentboxd`
- `tmux`
- 需要使用的 Runtime CLI：OMP、Codex、Claude Code

```text
Browser
  │ HTTP
  ▼
agentbox-server ─── PostgreSQL
  │ gRPC 9443
  ├──────── agentboxd · macOS Host A
  ├──────── agentboxd · Linux Host B
  └──────── agentboxd · Linux Host C
```

当前默认传输是 HTTP、WebSocket 和明文 gRPC。跨主机部署必须放在可信内网、VPN 或 Tailnet 中，并通过防火墙限制 `9443` 的访问来源；不要直接暴露到公网。

## 2. 支持的平台

正式 Release 提供：

- macOS arm64
- macOS amd64
- Linux arm64
- Linux amd64

Host 必须安装：

- `tmux`
- 至少一个 Runtime CLI
- Control Plane 额外需要 PostgreSQL 16 或兼容版本

推荐 Runtime 前置检查：

```bash
omp --version
codex --version
claude --version
```

## 3. 下载和校验 Release

Release 页面：

```text
https://github.com/foolzzz/abox/releases/tag/v0.6.0
```

根据操作系统和架构下载：

```text
agentbox-0.6.0-darwin-arm64.tar.gz
agentbox-0.6.0-darwin-amd64.tar.gz
agentbox-0.6.0-linux-arm64.tar.gz
agentbox-0.6.0-linux-amd64.tar.gz
checksums.txt
```

校验当前平台的压缩包。

macOS：

```bash
shasum -a 256 agentbox-0.6.0-darwin-arm64.tar.gz
```

Linux：

```bash
sha256sum agentbox-0.6.0-linux-amd64.tar.gz
```

输出必须与 `checksums.txt` 中对应文件一致。

解压：

```bash
tar -xzf agentbox-0.6.0-<os>-<arch>.tar.gz
cd agentbox-0.6.0-<os>-<arch>
```

## 4. 安装文件

建议先安装文件、修改配置，确认配置有效后再启动服务：

```bash
./install.sh
```

安装位置：

```text
~/.local/bin/agentbox-server
~/.local/bin/agentboxd
~/.agentbox/server.json
~/.agentbox/web/
~/.agentboxd/config.json
~/.agentboxd/state/
```

权限要求：

```text
~/.agentbox/                 0700
~/.agentbox/server.json      0600
~/.agentboxd/                0700
~/.agentboxd/config.json     0600
~/.agentboxd/state/          0700
```

安装脚本不会覆盖已经存在的 Server 或 daemon 配置。

`./install.sh --enable-services` 会直接启动服务，只适用于配置文件已经准备好的升级场景。首次安装不要在仍包含 `replace-me` 的情况下使用该参数。

## 5. PostgreSQL

Release 安装脚本不负责创建 PostgreSQL。可以使用现有 RDS/PostgreSQL，也可以在 Control Plane 主机上运行本地容器。

本地容器示例：

```bash
docker volume create agentbox-postgres

docker run -d \
  --name agentbox-postgres \
  --restart unless-stopped \
  -e POSTGRES_USER=agentbox \
  -e POSTGRES_PASSWORD='<strong-password>' \
  -e POSTGRES_DB=agentbox \
  -p 127.0.0.1:54329:5432 \
  -v agentbox-postgres:/var/lib/postgresql/data \
  postgres:16-alpine
```

确认数据库可用：

```bash
docker exec agentbox-postgres pg_isready -U agentbox -d agentbox
```

升级前必须备份数据库。数据库 Migration 在 Server 启动时执行；跨版本回滚不能假设旧二进制兼容新 Schema。

## 6. 配置 Control Plane

编辑：

```text
~/.agentbox/server.json
```

最小示例：

```json
{
  "databaseUrl": "postgres://agentbox:<password>@127.0.0.1:54329/agentbox?sslmode=disable",
  "httpAddr": "127.0.0.1:8080",
  "grpcAddr": "127.0.0.1:9443",
  "publicUrl": "http://127.0.0.1:8080",
  "enrollmentToken": "<random-enrollment-token>",
  "webDir": "~/.agentbox/web",
  "version": "0.6.0",
  "approvalPollInterval": "1s",
  "hibernationPollInterval": "30s",
  "schedulePollInterval": "1s",
  "retentionPollInterval": "1h",
  "operationalRetention": "720h",
  "auditRetention": "8760h",
  "reaperBatchSize": 100,
  "webhookSecret": "<random-webhook-secret>",
  "enableCodex": true,
  "enableClaude": true,
  "runtimeModels": {
    "omp": ["gpt-5.2"],
    "codex": ["gpt-5.3-codex"],
    "claude": ["claude-sonnet-4-6", "claude-opus-4-6"]
  }
}
```

生成随机 Secret：

```bash
openssl rand -hex 32
```

`enrollmentToken` 必须与每台新 Host 的 daemon 配置一致。Host 首次注册后会获得独立 Host Credential，后续重连使用 Host Credential。

远程部署时：

- `httpAddr` 绑定到 Web 用户能够访问的受信地址。
- `grpcAddr` 绑定到 daemon 能够访问的受信地址。
- `publicUrl` 设置为浏览器实际访问地址。
- 防火墙只允许可信 daemon 访问 gRPC 端口。

## 7. 配置 Host daemon

编辑：

```text
~/.agentboxd/config.json
```

示例：

```json
{
  "serverAddress": "127.0.0.1:9443",
  "serverTLS": false,
  "serverName": "",
  "hostId": "",
  "hostName": "My Mac Studio",
  "enrollmentToken": "<same-enrollment-token>",
  "stateDirectory": "~/.agentboxd/state",
  "workspaceRoots": ["~/projects"],
  "ompBinary": "omp",
  "codexBinary": "codex",
  "enableCodex": true,
  "tmuxBinary": "tmux",
  "claudeBinary": "claude",
  "enableClaude": true,
  "claudePermissionMode": "bypassPermissions",
  "healthAddress": "127.0.0.1:9091",
  "maxActiveBoxes": 4,
  "maxTerminalSessions": 8,
  "journalMaxBytes": 67108864,
  "journalMaxRecords": 16384,
  "journalMaxRecordBytes": 1048576,
  "idempotencyMaxFrames": 16384,
  "idempotencyMaxCommands": 8192,
  "maxRunDuration": "2h",
  "heartbeatInterval": "15s",
  "shutdownTimeout": "15s",
  "runtimeProbeTimeout": "10s",
  "runtimeStateRetention": "168h"
}
```

如果不设置 `workspaceRoots`，默认允许当前系统用户的 Home 目录。生产环境建议显式限制到项目根目录。

首次启动会自动生成稳定的 `hostId` 并写回 `0600` 配置。不要在不同物理主机之间复制已经生成 `hostId` 的 daemon 配置。

## 8. Runtime 认证

所有 Runtime 都以运行 `agentboxd` 的系统用户身份执行。认证用户必须与 daemon 用户一致。

### 8.1 OMP

在 daemon 用户下完成 OMP 自身认证，并确认：

```bash
omp --version
```

### 8.2 Codex

在 daemon 用户下执行：

```bash
codex login
codex --version
```

AgentBox 会启动 Codex App Server 并调用 `account/read`，未登录时不会将 Codex 广告为可用。

### 8.3 Claude OAuth / Keychain

在 daemon 用户下检查：

```bash
claude auth status --json
```

预期：

```json
{
  "loggedIn": true,
  "authMethod": "claude.ai"
}
```

`agentboxd` 不读取 OAuth Token，也不调用 `security unlock-keychain`。Claude CLI 自己访问当前用户的 Keychain 或凭据存储。

当 OAuth 已有效但 Claude 交互式 Onboarding 未完成时，daemon 会：

1. 备份 `~/.claude.json` 到 `~/.claude/backups/`。
2. 将备份权限设置为 `0600`。
3. 原子设置 `hasCompletedOnboarding=true`。
4. 写入当前 `lastOnboardingVersion`。
5. 保留 OAuth Account、Session、Plugin、MCP 和其他未知配置字段。

因此已经完成 OAuth 登录的 Host 不需要再次走浏览器登录。

macOS 必须将 daemon 安装为当前用户的 LaunchAgent，而不是 root LaunchDaemon。Linux 应使用完成 OAuth 登录的用户运行 systemd user service。

### 8.4 Claude API Key

Headless Host 可以在 `agentboxd` 的进程环境中提供：

```text
ANTHROPIC_API_KEY
```

不要把 API Key 写入：

- `server.json`
- `config.json`
- AgentBox 数据库
- Unit 的 `ExecStart` 参数
- Shell 历史中的明文命令

Linux systemd user service 可以使用权限为 `0600` 的 EnvironmentFile：

```bash
mkdir -p ~/.config/agentboxd
chmod 700 ~/.config/agentboxd
printf '%s\n' 'ANTHROPIC_API_KEY=<secret>' > ~/.config/agentboxd/claude.env
chmod 600 ~/.config/agentboxd/claude.env
systemctl --user edit agentboxd.service
```

Override：

```ini
[Service]
EnvironmentFile=%h/.config/agentboxd/claude.env
```

然后：

```bash
systemctl --user daemon-reload
systemctl --user restart agentboxd.service
```

macOS 推荐使用 OAuth/Keychain。若必须使用 API Key，应由 MDM 或 Secret Manager 在用户级 launchd 环境中注入，然后重新启动 daemon：

```bash
launchctl setenv ANTHROPIC_API_KEY "$ANTHROPIC_API_KEY"
launchctl kickstart -k "gui/$(id -u)/io.agentbox.daemon"
```

该环境变量由 tmux 的 `update-environment` 复制到新 Claude Agent Terminal。AgentBox 不读取、记录或持久化 Key 值。

### 8.5 恢复本机 Claude Session

新建 Claude Box 时可以选择“恢复已有会话”并填写 Claude Session UUID。AgentBox 在所选 Host 上启动：

Admin 选择 Host 后，AgentBox 会通过该 Host 的 daemon 查询本地 Claude Session，并按名称、Workspace 或 Session ID 搜索。选择结果会自动填写 UUID；若 Session 记录包含 Workspace，Admin 路径模式会自动填充该路径。发现接口只读取 Session ID、Workspace、名称、更新时间和运行状态，不读取对话正文。

如果 Host Offline、发现超时或 Claude 本地格式无法解析，页面保留手动 Session ID 输入，不阻塞恢复流程。普通 User 不获得 Host 全量 Session 列表，仍可在有 Workspace 权限时手动填写已知 Session ID。

```bash
claude --resume <session-id>
```

多个 Box 可以使用同一个 Session Ref；每个 Box 仍拥有独立的 AgentBox tmux。删除 Box 只终止对应 tmux/Runtime，不删除 Claude 本地历史。恢复会继承原对话及 System Prompt Snapshot，当前 Agent Definition 不覆盖原历史。

Box 列表默认只显示当前用户创建的 Box，可以通过 Owner 筛选切换其他 Organization 用户或全部可见 Box。

Host 可以注册任意数量 Box。`maxActiveBoxes` 只限制同时运行的 Runtime 数量，不限制 Box 记录数量。

删除 Offline Host 时可以显式选择级联依赖。级联会终止该 Host 的 Box、删除 Schedule、归档 Workspace、取消未完成运行和命令，再吊销 Host Credential；该操作不可撤销，执行前必须确认数据库备份和影响范围。

## 9. macOS 启动服务

确认配置完成后：

```bash
domain="gui/$(id -u)"
launchctl bootstrap "$domain" ~/Library/LaunchAgents/io.agentbox.server.plist
launchctl bootstrap "$domain" ~/Library/LaunchAgents/io.agentbox.daemon.plist
```

单独重启：

```bash
launchctl kickstart -k "gui/$(id -u)/io.agentbox.server"
launchctl kickstart -k "gui/$(id -u)/io.agentbox.daemon"
```

查看状态：

```bash
launchctl print "gui/$(id -u)/io.agentbox.server"
launchctl print "gui/$(id -u)/io.agentbox.daemon"
```

日志：

```text
~/.agentbox/server.stdout.log
~/.agentbox/server.stderr.log
~/.agentboxd/agentboxd.stdout.log
~/.agentboxd/agentboxd.stderr.log
```

只部署 Worker Host 时，只 bootstrap `io.agentbox.daemon`。

## 10. Linux 启动服务

确认配置完成后：

```bash
systemctl --user daemon-reload
systemctl --user enable --now agentbox-server.service agentboxd.service
```

查看状态：

```bash
systemctl --user status agentbox-server.service
systemctl --user status agentboxd.service
```

查看日志：

```bash
journalctl --user -u agentbox-server.service -f
journalctl --user -u agentboxd.service -f
```

只部署 Worker Host：

```bash
systemctl --user enable --now agentboxd.service
```

若需要用户退出登录后继续运行 systemd user service：

```bash
loginctl enable-linger "$USER"
```

该命令通常需要系统管理员权限。

## 11. 首次登录

空数据库首次启动会创建临时管理员账号：

```text
Username: admin
Password: admin123
```

首次登录后必须立即修改密码。不要把临时密码暴露在页面、部署日志、工单或共享文档中。

打开：

```text
http://<control-plane-host>:8080
```

## 12. 健康检查

Control Plane：

```bash
curl -fsS http://127.0.0.1:8080/readyz
```

预期：

```json
{"status":"ready"}
```

daemon：

```bash
curl -fsS http://127.0.0.1:9091/readyz
```

预期关键字段：

```json
{
  "status": "ready",
  "connected": true,
  "runtimeAvailable": true
}
```

详细诊断：

```bash
curl -fsS http://127.0.0.1:9091/diagnostics
```

确认 Host Runtime：

```text
OMP      available
Codex    available
Claude   available
```

如果 Agent 创建弹窗在 Host 重连前已经打开，它会每 5 秒自动刷新 Runtime 可用性。

## 13. 升级

升级前：

1. 备份 PostgreSQL。
2. 备份 `~/.agentbox/server.json`。
3. 备份 `~/.agentboxd/config.json` 和 `~/.agentboxd/state/`。
4. 下载并校验新 Release。

在解压后的新 Release 目录执行：

```bash
./install.sh
```

该操作更新二进制和 Web Assets，保留已有配置和 daemon state。

推荐重启顺序：

```text
1. agentbox-server
2. agentboxd
```

macOS：

```bash
launchctl kickstart -k "gui/$(id -u)/io.agentbox.server"
launchctl kickstart -k "gui/$(id -u)/io.agentbox.daemon"
```

Linux：

```bash
systemctl --user restart agentbox-server.service
systemctl --user restart agentboxd.service
```

升级后执行健康检查，并确认 daemon 已重新连接。

## 14. 回滚

二进制和 Web Assets 可以通过安装旧 Release 覆盖回滚：

```bash
cd agentbox-<old-version>-<os>-<arch>
./install.sh
```

然后重启服务。

数据库 Migration 是前向执行的。若新版本包含不兼容 Schema 变化，不能只回滚二进制；必须按变更说明恢复升级前数据库备份。

## 15. 卸载

保留配置和 daemon state：

```bash
./uninstall.sh
```

同时删除配置和 daemon state：

```bash
./uninstall.sh --purge
```

`--purge` 不会删除外部 PostgreSQL 数据库或 Docker Volume，需要单独处理。

## 16. 常见故障

### Runtime 全部显示不可用

检查：

```bash
curl -fsS http://127.0.0.1:9091/diagnostics
```

重点关注：

```text
readiness.serverConnection
readiness.runtime
lastFailures
```

然后确认 daemon 配置中的 `serverAddress`、Enrollment Token、网络 ACL 和 Server gRPC 监听地址。

### Claude 显示不可用

检查：

```bash
claude --version
claude auth status --json
```

若使用 API Key，确认变量存在于 daemon Service 环境，而不只是当前交互 Shell。

### Claude Agent Terminal 再次显示登录

确认：

```bash
jq '{hasCompletedOnboarding,lastOnboardingVersion}' ~/.claude.json
```

如果旧 tmux Session 是修复前创建的，需要终止该 Session 后重新打开 Box。新进程才会读取更新后的 Onboarding 状态。

### Host 长期 Offline

检查 daemon 日志和：

```bash
curl -fsS http://127.0.0.1:9091/readyz
```

`v0.4.2` 起，Server Frame ID 使用有界滚动保留，不会再因 Idempotency Store 达到 16,384 条而持续断线。
