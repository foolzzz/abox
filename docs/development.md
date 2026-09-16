# Local Development

## Prerequisites

- Go 1.24+
- Node.js 22+
- Docker 28+
- protoc 28+
- OMP in `PATH`
- Claude Code in `PATH` for Phase 0/2 conformance tests

The repository uses npm workspaces; pnpm is not required.

## Bootstrap

```bash
cp .env.example .env
npm install
go mod download
make generate
make postgres-up
make test
make build
```

## Run

```bash
set -a; source .env; set +a
make dev-server
```

In another terminal:

```bash
set -a; source .env; set +a
make dev-daemon
```

Open `http://127.0.0.1:8080`.

## Git milestones

Development is recorded locally on `main`. Each completed phase receives a commit before the next phase starts.
