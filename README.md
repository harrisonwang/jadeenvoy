<p align="center">
  <strong>JadeEnvoy</strong>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/Go-1.23+-00ADD8?logo=go&logoColor=white" alt="Go 1.23+" />
  <img src="https://img.shields.io/badge/License-Apache_2.0-blue.svg" alt="Apache 2.0 License" />
  <img src="https://img.shields.io/badge/Status-WIP-orange" alt="WIP" />
</p>

# JadeEnvoy

> An open managed-agents platform — self-host first, in Go.

> [中文 README](README.zh-CN.md)

**Status: 🚧 Work in progress. V1 runtime core, Vault + MITM credential injection, and Auth (cookie + API key) are implemented and covered by e2e tests; several M2 features are experimental.**

Implemented today:

- `jed` daemon with SQLite persistence, event log, SSE, metrics, and LLM providers `mock` / `openai_compat` / `anthropic` / `anthropic_compat` (all hand-written thin clients, no SDK).
- Agent/session runtime loop with subprocess sandbox and built-in `bash`, `read`, `write`, `edit`, `glob`, `grep` tools.
- Core e2e flow: user message → model request → tool use → tool result → final agent message.
- **Vault** (`static_bearer`) with AES-256-GCM storage, and the **`je-vault` HTTPS MITM proxy** that strips dummy client credentials and injects real tokens into sandbox egress.
- **Auth**: cookie sessions + API keys with `AUTH_MODE` (`required`/`optional`/`bypass`) — all standard-library crypto, zero third-party deps (see ADR-0019).
- **`je` CLI**: agents / sessions (with streaming `send`) / vaults / api-keys over REST — stdlib `flag`, no cobra.
- Experimental M2 APIs for files, memory stores, skills, custom tools, session resources, and outbound webhooks.

Not complete yet:

- Console UI (fork of OMA), Postgres backend, the GitLab review adapter demo, and one-shot docker-compose are not started.
- Some compatibility endpoints beyond the V1 runtime path may still be incomplete.

JadeEnvoy is a managed-agents runtime: write an agent (model + system prompt
+ tools), and the platform handles the loop — sessions, sandbox execution,
streaming events, credential injection, persistent history.

API-compatible with [Anthropic Managed Agents](https://platform.claude.com/docs/en/managed-agents/),
runs entirely on your own infrastructure.

## Why

Anthropic's [open-managed-agents](https://github.com/open-ma/open-managed-agents)
(OMA) is excellent but TypeScript + Cloudflare-first; the Node self-host path
has structural gaps for fully-internal deployments.

JadeEnvoy is a **clean reimplementation in Go**, focused on:

- **Self-host first** — no Cloudflare dependency, single binary + Docker
- **Internal LLM friendly** — first-class OpenAI-compatible support for
  on-prem / private gateways
- **Operationally simple** — pure Go, no CGo, no pnpm version churn
- **API-compatible** — existing Anthropic SDK works against JadeEnvoy with
  only a base_url change

See [`.docs/00-motivation/why-jadeenvoy.md`](.docs/00-motivation/why-jadeenvoy.md)
for the full rationale.

## Architecture (overview)

```
┌──────────────┐    ┌──────────────┐    ┌──────────────────┐
│ Console UI   │    │   je CLI     │    │ Webhook adapter  │
│  (React)     │    │  (cobra)     │    │  (GitLab/Slack…) │
└──────┬───────┘    └──────┬───────┘    └─────────┬────────┘
       │                   │                      │
       └───────────────────┼──────────────────────┘
                           │ HTTP / SSE
                  ┌────────▼────────┐
                  │      jed         │  ← Go daemon
                  │  /v1/*  /admin/* │
                  └────────┬─────────┘
                ┌──────────┴──────────┐
                ▼                     ▼
       ┌──────────────┐      ┌──────────────┐
       │   je-vault    │     │  Sandbox     │
       │  MITM proxy   │     │ subprocess   │
       └───────────────┘     └──────────────┘
                ▼
       SQLite or Postgres
```

Details: [`.docs/20-architecture/overview.md`](.docs/20-architecture/overview.md).

## Roadmap

| Milestone | Window | Theme |
|---|---|---|
| **M1 — V1 MVP** | 4 weeks | GitLab code review bot, end-to-end |
| **M2 — Post-MVP** | +2-3 months | Full tools, Memory, Skills, MCP, Docker sandbox, Webhooks |
| **M3 — Mature** | +6 months | Multi-tenant, OAuth credentials, Multi-agent, UI rewrite |

Detailed backlog: [`.docs/10-feature-backlog/`](.docs/10-feature-backlog/).

## Components

| Binary | Role |
|---|---|
| `jed` | Main daemon — REST API, agent orchestration, harness loop |
| `je` | CLI client — agents/sessions/vaults/api-keys over REST (stdlib `flag`, ADR-0019) |
| `je-vault` | HTTPS MITM proxy sidecar — injects vault credentials into sandbox egress (stdlib, ADR-0019) |

## Quick start

```bash
make build
JE_AUTH_MODE=bypass JE_LLM_PROVIDER=mock go run ./cmd/jed
curl localhost:8787/health
```

For a realistic local loop, use the e2e tests as executable examples:

```bash
go test ./test/e2e/... -run TestE2E_BashToolUse -v -count=1
```

To point `jed` at an OpenAI-compatible gateway:

```bash
JE_LLM_PROVIDER=openai_compat \
JE_LLM_BASE_URL=https://your-gateway.example.com/v1 \
JE_LLM_API_KEY=... \
go run ./cmd/jed
```

## Documentation

- **User docs** — coming with V1 ship (`docs/`, published to `docs.jadeenvoy.com`)
- **Internal docs (engineering)** — [`.docs/`](.docs/)
  - [Motivation](.docs/00-motivation/)
  - [Feature backlog by milestone](.docs/10-feature-backlog/)
  - [Architecture](.docs/20-architecture/)
  - [ADRs](.docs/30-adr/)
  - [Implementation notes](.docs/40-implementation-notes/)

## Contributing

Stay tuned. Once V1 ships, contribution guide and CLA/DCO process will be
documented in `CONTRIBUTING.md`.

For now, design proposals welcome as issues.

## License

[Apache 2.0](LICENSE).

JadeEnvoy Console UI is forked from [open-ma/open-managed-agents](https://github.com/open-ma/open-managed-agents)
(Apache 2.0). See [NOTICE](NOTICE) for attribution.
