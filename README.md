# ppts — AI-Narrated Presentation Platform

> Turn a static PPTX into a **self-narrating** asset: AI script + production TTS voice + slide-synced
> subtitles — a complete narrated deck you can audition, edit slide by slide, and export to video.

🌐 **English** | [简体中文](README.zh-CN.md)

![Go](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go)
![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)
[![CI](https://github.com/F31/ppts/actions/workflows/ci.yml/badge.svg)](https://github.com/F31/ppts/actions/workflows/ci.yml)

![Narration page: slide navigator + three script modes](assets/demo/ppts1.png)

![Script editing and voice playback: segment-level editing, player with timeline and subtitles](assets/demo/ppts2.png)

---

## Table of Contents

- [Overview](#overview)
- [Key Advantages](#key-advantages)
- [Features](#features)
- [Quick Start](#quick-start)
- [Deployment](#deployment)
- [Architecture](#architecture)
- [Configuration](#configuration)
- [Observability](#observability)
- [Security](#security)
- [Development](#development)
- [Documentation](#documentation)
- [Contributing and License](#contributing-and-license)

---

## Overview

ppts solves a plain but universal problem: **the deck is done — but nobody is there to present it.**

> The proposal you emailed never gets read properly. The onboarding material has to be presented by you
> for the twentieth time. You want to publish it as a video, a podcast episode, or an accessible asset —
> and then you discover it still needs screen recording, voice-over recording and manual subtitling.
> Expensive enough that you just give up.

ppts pulls that entire chain into one closed pipeline:

```text
Upload PPTX ──▶ Parse slides/elements ──▶ Generate AI script ──▶ Production TTS ──▶ Align subtitles & timeline
                    │                                                                   │
                    └────── Optional slide rendering (LibreOffice → PDF → PNG)           ▼
                                                                 Export: Web deck / MP4 / SRT / VTT
```

**Who it is for**

- Content, training and product teams: turn existing decks into listenable, watchable assets in bulk.
- Teams repurposing decks into video or localized formats: export SRT / VTT straight into an editor.
- Go developers: every capability is exposed as Connect RPC and REST endpoints, embeddable in your own platform.

**Technology base**: PPTX parsing comes from the companion pure-Go SDK
[go-pptx](https://github.com/F31/go-pptx) (v2.0.3); TTS text normalization is handled by the standalone
[textnorm](https://github.com/F31/textnorm) component. Together they form the three layers
**SDK → component → platform**.

---

## Key Advantages

| # | Advantage | Notes |
|---|---|---|
| 1 | **One pipeline, not a tool grab bag** | Upload, parse, script, voice, subtitles and export are integrated; a single tenant model and job state machine spans the whole chain |
| 2 | **AI scripts you can trust** | After generation, a deterministic entity validator checks numbers / units / dates / model names. If something drifts, at most one targeted correction is attempted; if it still drifts, the system **falls back to the original text** — better no AI than an unapproved rewrite of your numbers |
| 3 | **Traceable provenance** | Every script segment carries a structured source anchor (which shape / note / slide image it came from). Anchors live server-side; client-supplied anchors are rejected |
| 4 | **Cost-aware** | Segment audio is content-addressed and cached, so identical synthesis settings are reused across slides and projects — zero TTS calls, zero duplicate storage |
| 5 | **Deploys at your scale** | Same binary: SQLite mode works out of the box with no config; PostgreSQL mode is a full multi-tenant SaaS backend |
| 6 | **Operable** | Multi-tenancy with five roles, quota and billing split (user price vs. provider cost), audit trails, full data export and hard erasure |
| 7 | **Engineering receipts** | HTTP end-to-end tests against real PostgreSQL (real workers, real storage), a 100-page evaluation corpus with an AI entity-validation gate, and five automated frontend gates |

---

## Features

### Content and scripts

- **Three script modes**: verbatim reading / polished narration (written → spoken register) / AI-generated narration (adds background and explanation)
- **Segment-level editing**: shorten, polish, bridge, **pronounce** (tenant-level pronunciation dictionary fixing misreads of CUDA / MySQL / Kubernetes and friends), and pause
- **Optimistic concurrency**: saves carry `expected_revision`; on conflict the backend returns the latest version and the frontend loads it with a notice
- **Source anchors**: a structural channel (`shape_*`, confidence 1.0) plus a visual channel (`visual_*`, low confidence, flagged for review)

### Narration and export

- **Production TTS provider**: SiliconFlow (CosyVoice2) by default over an OpenAI-compatible interface — swap providers with one env var
- **Segment-level regeneration**: only changed segments are re-synthesized; untouched ones hit the cache at zero TTS cost
- **Target-duration loop**: after the first pass the rate deviation is measured; beyond ±10% the speaking rate is adjusted proportionally and the segment re-synthesized
- **Export formats**: web narrated deck (zip: timeline + subtitles + audio segments) / MP4 / SRT / VTT / narrated PPTX (audio + auto-advance embedded in a copy of the source deck)
- **Export duration cap**: `PPTS_MAX_EXPORT_DURATION_MINUTES` (default 60, `0` disables) rejects overlong timelines before any artifact is written
- **Slide image rendering**: LibreOffice → PDF → PNG, optional enhancement. Render failure only marks that step failed; parsing still succeeds

### Platform capabilities

- **Multi-tenancy**: PostgreSQL row-level security (`FORCE RLS`) with five roles (Owner / Admin / Editor / Reviewer / Viewer)
- **Model gateway**: TTS / LLM endpoints, models and voices configured from the web UI at both tenant and platform level
- **Quota and billing**: metered by tokens / generated seconds with an idempotent ledger; **provider cost and user billing are tracked separately**
- **Audit and compliance**: operation auditing (coverage matrix extracted from source AST and gated in CI), object storage with expiry archiving, full tenant data export and erasure
- **Three storage backends**: local / S3-compatible / tenant-supplied credentials (BYOS), with optional server-side envelope encryption

### Connect RPC services

| Service | Responsibility |
|---|---|
| `ProjectService` | Create / read / list / archive projects, list slides |
| `UploadService` | Authorized direct upload: create / complete / abort |
| `ScriptService` | Read / update / approve / lock scripts, draft generation |
| `NarrationService` | Create narration generation jobs |
| `PlaybackService` | Narration timeline and playback manifest (audio + subtitles + slide images) |
| `ExportService` | Create export, read artifact, create download link |
| `JobService` | Job query, cancel, retry and event stream |
| `TenantService` | Member roles, usage, audit, export and erasure |

---

## Quick Start

### Option 1: Docker Compose (recommended first try)

```bash
git clone https://github.com/F31/ppts.git && cd ppts
cd web && npm ci && npm run build && cd ..   # the frontend build is baked into the api image
docker compose build
docker compose up -d
# API + console: http://localhost  (health check http://localhost/healthz)
```

> If you write your own orchestration: the api binds to privileged port 80, and the api service in
> `docker-compose.yml` carries `cap_add: [NET_BIND_SERVICE]`. **Omit that and the process exits with
> `EACCES` at bind time.**

Images: api ≈ **168MB** (a single Go process serving both API and static frontend assets — no nginx needed),
worker ≈ **820MB** (includes LibreOffice, poppler, CJK subtitle fonts and static ffmpeg). Both run as
uid 10001, non-root.

### Option 2: Single binary (SQLite, zero config)

```bash
scripts/package.sh [version]       # defaults to git describe
tar xzf dist/ppts-<version>-linux-amd64.tar.gz && cd ppts-<version>-linux-amd64
sudo ./install.sh                  # deps → account → config → migrate → systemd start
```

The release tarball ships the single entrypoint binary `ppts` (fully static, `CGO_ENABLED=0`), static
ffmpeg/ffprobe, `install.sh`, systemd units and config templates. **The frontend build and SQL migrations
are embedded in the binary** — one file is enough to deploy.

Four subcommands:

| Command | Purpose |
|---|---|
| `ppts server` | API + web console (listens on `:8080` by default, override with `PPTS_HTTP_ADDR`) |
| `ppts worker` | Job executor (parse / script / narrate / export) |
| `ppts migrate` | Idempotently apply embedded migrations |
| `ppts version` | Print version |

Minimal local start (SQLite default, data in `~/.ppts/ppts.db`, no login):

```bash
PPTS_OBJECT_BACKEND=local PPTS_OBJECT_ROOT=./var/ppts-objects \
PPTS_OBJECT_SECRET="$(head -c32 /dev/urandom | base64)" \
GOWORK=off go run ./cmd/ppts server
```

Then open <http://localhost:8080>.

### Option 3: PostgreSQL production mode

```bash
PPTS_DATABASE_URL='postgres://ppts_app:...@host:5432/ppts' \
PPTS_OBJECT_ROOT=./var/ppts-objects \
PPTS_OBJECT_SECRET='dev-download-secret' \
GOWORK=off go run ./cmd/ppts server

PPTS_DATABASE_URL='postgres://ppts_app:...@host:5432/ppts' \
PPTS_TENANT_ID='00000000-0000-0000-0000-000000000000' \
PPTS_TTS_PROVIDER='fake' \
GOWORK=off go run ./cmd/ppts worker
```

> **The database account must be a restricted role that does not own the tables.** Migrations run as
> `ppts_migrator`, which holds owner rights; the runtime account `ppts_app` must be
> `NOSUPERUSER NOBYPASSRLS`, otherwise row-level security is bypassed. See [Security](#security).

---

## Deployment

### Install script

`install.sh` **is idempotent and safe to re-run**; it never overwrites existing configuration.

```bash
SKIP_DEPS=1  sudo ./install.sh     # skip system dependency installation
SKIP_START=1 sudo ./install.sh     # install only, do not start
```

Layout: `/opt/ppts/bin`, `/etc/ppts/config.env`, `/var/lib/ppts` (local object storage and LibreOffice profile).
Systemd units: `packaging/ppts-api.service` and `packaging/ppts-worker.service`.

> ⚠️ The bundled ffmpeg is a static build containing GPL components (x264 / libass).
> **Verify license compliance before commercial distribution.**

### External command dependencies

| Capability | Dependency | Behavior when missing |
|---|---|---|
| Slide image rendering | `soffice` (LibreOffice), `pdftoppm` / `pdfinfo` (poppler) | Rendering is an optional enhancement and fails; parsing still succeeds and artifacts may lack slide images |
| A/V muxing / MP4 export | `ffmpeg` / `ffprobe` | The corresponding export format is unavailable |

Binary paths can be overridden with `PPTS_SOFFICE_BIN` / `PPTS_PDFTOPPPM_BIN` / `PPTS_PDFINFO_BIN`.
When invoking soffice through WSL interop on Windows, `PPTS_RENDER_WORK_ROOT` must point to a path
under `/mnt/<drive>`.

---

## Architecture

```text
┌──────────────────────────────────────────────────────────────┐
│  Console (React + Vite + TypeScript)                          │
│  OIDC PKCE login / projects / narration / jobs / settings     │
└───────────────────────────────┬──────────────────────────────┘
                                │ Connect RPC + REST
┌───────────────────────────────▼──────────────────────────────┐
│  API (cmd/ppts server)                                        │
│  8 Connect services · model gateway · object signing · audit  │
└───────────────┬───────────────────────────┬───────────────────┘
                │ enqueue                    │ read/write
┌───────────────▼───────────────┐   ┌───────▼───────────────────┐
│  Worker (cmd/ppts worker)      │   │  Storage                   │
│  parse / script / voice /export│   │  SQLite or PostgreSQL (RLS)│
│  lease · retry · cancel ·      │   │  Objects: local / S3 / BYOS│
│  progress · retention · orphan │   │                            │
└───────────────┬───────────────┘   └────────────────────────────┘
                │
        ┌───────┴────────┬──────────────┬──────────────┐
        ▼                ▼              ▼              ▼
    go-pptx          textnorm       LLM provider    TTS provider
  (PPTX read/write) (text normalize) (OpenAI-compat) (OpenAI-compat)
```

### Reliability semantics of the job pipeline

- **State / retry / lease / cancel**: managed centrally by `internal/pipeline`; `ppts_claim_next_job` is a
  restricted function, so the scheduler account has no generic read access to business tables
- **Progress reporting**: the worker injects `pipeline.ReportProgress`; the handler call performs a
  fenced conditional update. Narration reports 0–100% by synthesized segments
- **Outbox**: `SetCommitStep` declares "final artifact steps that only exist once complete"; the worker
  upserts them atomically **in the same transaction** as the job terminal state via `CompleteWithStep` —
  there is no crash window where the step succeeded but the job never reached a terminal state
- **Event stream**: `WatchEvents` uses a monotonic `seq` on a dedicated event table; clients resume with
  `after_seq` (timestamps are deliberately not reused, which avoids dropping concurrent events in the same millisecond)

### Directory layout

```text
cmd/ppts            Single entrypoint: server / worker / migrate / version
cmd/local-engine    Desktop Go sidecar
internal/project    Source file revisions / slides / elements / import report + go-pptx read adapter
internal/narration  Scripts / segments / source anchors / terminology / approval
internal/pipeline   Job state / retry / lease / cancel / events / outbox
internal/media      Audio tracks / subtitles / timeline / export
internal/tenant     Identity / roles / policy / RLS
internal/usage      Quota / settlement / ledger / cost
internal/integrations  LLM / TTS / rendering / storage adapters
proto/ppts/v1       Connect contracts (8 services)
migrations          SQL migrations (expand → migrate → contract)
web/                Frontend workspace (Vite + React + TypeScript)
docs/               Technical design / ADRs / runbooks / tracking plans
```

---

## Configuration

Everything is configured via environment variables (or `/etc/ppts/config.env` generated by the installer).
Most used:

| Variable | Purpose |
|---|---|
| `PPTS_DB_DRIVER` | `sqlite` (default, lightweight single tenant) or `postgres` (full multi-tenant) |
| `PPTS_DATABASE_URL` | PostgreSQL runtime connection string (**must be a restricted, non-owner account**) |
| `PPTS_MIGRATE_DATABASE_URL` | Migration DSN (needs higher privileges; falls back to `PPTS_DATABASE_URL`) |
| `PPTS_HTTP_ADDR` | API listen address, default `:8080` |
| `PPTS_OBJECT_BACKEND` | `local` (default) / `s3` / `byos:<credential_id>` |
| `PPTS_OBJECT_ROOT` | Root directory for local object storage |
| `PPTS_OBJECT_SECRET` | Signing key for local links |
| `PPTS_TTS_PROVIDER` | `fake` (development) / `siliconflow` (production) |
| `PPTS_LLM_PROVIDER` | `siliconflow` (currently supports OpenAI-compatible endpoints) |
| `PPTS_GATEWAY_AES_KEY_BASE64` | Encryption key for model gateway credentials |
| `PPTS_METRICS_TOKEN` | Bearer token for `/metrics` and `/debug/vars` (**unconfigured means denied**) |
| `PPTS_OTEL_EXPORTER_OTLP_ENDPOINT` | When set, traces are exported via OTLP; otherwise a no-op |

### Identity and login

Registration limits are per network egress IP: `PPTS_AUTH_REGISTER_PER_HOUR` limits
all registration requests (default **60/hour**, positive values only), while
`PPTS_REGISTER_DAILY_PER_IP` limits **successful account creations** (default
**20/day**, `0` disables this daily cap). Failed validation or account creation
does not consume the daily allowance. Success counts commit atomically with the
account, preventing concurrent requests from exceeding the cap. Windows are UTC
aligned; a daily window resets at 00:00 UTC (08:00 China time). When both limits
apply, the response reports the longer wait via `Retry-After`. The new success
counter is separate from historical attempt counts and begins on deployment.
Raise these configurable caps as appropriate for shared campus/company networks.

- SQLite mode has no login (fixed local tenant / user) and works out of the box
- PostgreSQL mode supports production OIDC: set `PPTS_OIDC_ISSUER` / `PPTS_OIDC_CLIENT_ID` and the API validates
  signatures via issuer discovery + JWKS. Tenant and user default to the `tenant_id` and `sub` claims
  (override with `PPTS_OIDC_TENANT_CLAIM` / `PPTS_OIDC_USER_CLAIM`)
- Once OIDC is configured, development identity headers are **denied by default**; only local development may
  explicitly set `PPTS_AUTH_DEV_HEADERS=true` to accept `X-PPTS-Tenant-ID` / `X-PPTS-User-ID`
- Web-side OIDC PKCE uses Vite env vars: `VITE_OIDC_AUTHORIZATION_ENDPOINT` / `VITE_OIDC_TOKEN_ENDPOINT` / `VITE_OIDC_CLIENT_ID`

### TTS / LLM providers

Both speak OpenAI-compatible HTTP and can be configured from the top-bar **Model Gateway**. Resolution order is
**tenant default → platform default → environment variable**, cached for 30s. Gateway API keys are stored
AES-GCM encrypted; read APIs only return `hasKey` / `keyMasked`.

| Provider | Default model | Notes |
|---|---|---|
| TTS `siliconflow` | `FunAudioLLM/CosyVoice2-0.5B` | voice is `<model>:<voice>`; 429 / 5xx are mapped to retryable |
| LLM `siliconflow` | `Qwen/Qwen2.5-7B-Instruct` | vision model defaults to `Qwen/Qwen3-VL-8B-Instruct` for slide understanding |
| TTS `fake` | — | Produces silent WAV only; **not a valid production provider acceptance test** |

### Storage

- `local`: local directory with server-signed links
- `s3`: S3-compatible; the tenant's `policy.storage_region` can route to `{bucket}-{region}` (lazily created and cached)
- `byos:<credential_id>`: tenant-supplied credentials, AES-GCM encrypted at rest with AAD bound to tenant / credential / backend
- **Envelope encryption**: set `PPTS_OBJECT_ENCRYPTION_KEY_BASE64` to encrypt/decrypt objects server-side for tenants
  that enable the policy (direct pre-signed reads/writes are unsupported once enabled)
- Lifecycle is driven by tenant policy (`storage_transition_days` / `storage_expiration_days`); source object retention,
  derived-file expiry, abandoned-upload cleanup and quota reservation reclamation run periodically in the worker
- **Orphan object scanning only reports, it does not delete** (writes an `orphan.detected` audit event). Review the
  candidates, then set `PPTS_ORPHAN_DELETE=1` for actual deletion — object deletion is irreversible, and "no references found"
  is a heuristic judgment

---

## Observability

- **Structured logs**: slog JSON with `X-Request-ID` per request, emitting `request_id / method / path / status / duration_ms / tenant / user`
- **Tracing**: the API emits spans via a Connect interceptor and persists the W3C `traceparent` into `jobs.traceparent`
  when a job is created; the worker links it as a SpanLink — **cross-process asynchronous tracing**. Without OTLP it is a
  zero-dependency no-op
- **Metrics**: `/metrics` requires `Bearer $PPTS_METRICS_TOKEN`; aggregation defaults to kind × event to avoid high
  cardinality. Enable `PPTS_METRICS_TENANT_LABELS=true` when troubleshooting
- **Queue view**: depth / running / backoff / oldest-wait metrics split by job type (replicas read the same table rather
  than each keeping their own view). **"Never appeared" must be distinguishable from "currently zero"** — a kind that has
  not been seen has no key at all. To judge whether a kind is still in use, look at the `ppts_worker_jobs_total` counter,
  not the depth gauge
- **Degradation indicators** (if present they need action; these are not optional optimizations):

  | Metric | Meaning |
  |---|---|
  | `ppts_tts_fake_provider_active` | Fake TTS in use → the audio produced is synthetic silence |
  | `ppts_render_pages_disabled` | Rendering disabled → artifacts may lack slide images |
  | `ppts_auth_dev_headers_active` | Dev identity headers accepted → identity can be forged; must never appear in production |
  | `ppts_ratelimit_degraded=1` | Rate limiting degraded to single-replica counting → effective allowance = limit × replica count |

- **Health check**: `GET /healthz` actually probes the database and object storage and returns 503 if either is
  unavailable. The response **contains only per-component status, never error reasons** (the endpoint is unauthenticated;
  raw errors go to server logs only). When the backend cannot be probed cheaply it reports `unsupported`, meaning
  "not checked" rather than healthy
- SLOs and alerting rules live in `docs/runbooks/SLO与告警.md`

---

## Security

- **Row-level security**: migrations run as the owner role while the runtime account must be
  `NOSUPERUSER NOBYPASSRLS`; tenant business tables use `FORCE ROW LEVEL SECURITY` with policies keyed on the
  transaction-local `app.tenant_id`. Missing context means access denied
- **Fail closed**: `/metrics` and `/debug/vars` are denied unless a token is configured (forgetting it means denied,
  not exposed). `/debug/pprof/*` is not mounted at all unless `PPTS_PPROF=true`, and then still requires auth
- **Secret handling**: gateway credentials, BYOS credentials and the object envelope key are all AES-GCM.
  Audit records deliberately omit token / publicId / IP / user-agent so short-lived credentials are not copied into
  longer-lived retention
- **Audit**: `audit_events` is tenant-isolated and retained; matured events are archived to object storage as JSONL
- **Data compliance**: `ExportTenant` / `PurgeTenant` (owner permission) provide full tenant data export and erasure
- Role definitions live in `internal/api/roles.go`; the high-risk operation coverage matrix is in
  `docs/审计覆盖矩阵.md`

---

## Development

> The repo contains a `go.work` (wiring `../go-pptx` and the local `./textnorm` component).
> CI and releases build with `GOWORK=off`.

### Backend

```bash
GOWORK=off go build ./...
GOWORK=off go vet ./...
GOWORK=off go test ./...

# Tests requiring PostgreSQL (shared test database, must be serial)
PPTS_TEST_DATABASE='postgres://.../ppts_test' GOWORK=off go test -tags=pg -p 1 ./... -count=1
```

> **Two rules for reading test output**:
> ① A plain `go test ./...` does **not** include PostgreSQL tests — they are gated behind `-tags=pg` and only run in
> CI's `postgres` job, so the "ok package count" is not proof that every test executed.
> ② Cases depending on external tools or credentials (ffmpeg, poppler, S3, real audio) always `t.Skip` rather than fail.
> **Read the SKIP count together with ok / FAIL** — a rising SKIP count usually means the environment is missing
> something, not that you got lucky.

### Frontend

```bash
cd web
npm ci
npm run build       # = contrast → api:check → route:check → core:test → polling:test → tsc -b → vite build
npm run i18n:check  # not part of build; run separately (CI's web job has a dedicated step)
```

Gate meanings: `contrast` = contrast contract, `api:check` = single request egress contract, `route:check` = route manifest,
`core:test` / `polling:test` = unit behavior tests, `i18n:check` = ZH/EN key parity and orphan keys.

### Evaluation and ops scripts

| Script | Purpose |
|---|---|
| `scripts/gen_corpus` | Generate 13 suites totaling 100 pages (with number / unit / model entities) |
| `scripts/ai_eval` | AI evaluation gate: after per-slide LLM polishing, the entity validator checks whether key numbers stayed faithful to the source — **any drift fails the gate** |
| `scripts/replay_eval` | Validation-rule replay gate (non-rewriting; zero flips against the baseline) |
| `scripts/audit_coverage` | Generates `docs/审计覆盖矩阵.md` from the source AST (do not edit by hand) |
| `scripts/backup_restore_drill.sh` | Backup / restore drill |

---

## Documentation

> Note: the documents below live in the local `docs/` directory and are **not** distributed on GitHub.
> Clone the repository to read them; the paths are relative to the repo root.

| Document | Contents |
|---|---|
| `docs/PPTS功能与API说明书.md` | Features and API reference |
| `docs/PPT自动讲解工具-技术方案-V4.0.md` | Current technical baseline design |
| `docs/SQLite单租户部署.md` | SQLite mode capability boundaries and configuration |
| `docs/runbooks/SLO与告警.md` | Alert rules and degradation handling |
| `docs/审计覆盖矩阵.md` | Script-generated coverage matrix of high-risk operations |
| `docs/adr/` | Architecture decision records |
| `docs/PPT智能语音讲解平台-控制台产品优化实施计划-V1.0.md` | Console evolution plan |

---

## Contributing and License

Issues and PRs are welcome. Please run the backend and frontend verification loops above before submitting;
changes touching request egress, routes or i18n must keep the corresponding gates green.

Related projects:

- [go-pptx](https://github.com/F31/go-pptx) — the pure-Go PPTX SDK used by this project (Apache-2.0)
- [textnorm](https://github.com/F31/textnorm) — TTS text normalization engine (standalone component)

**License: Apache License 2.0** (see [LICENSE](LICENSE)).

Contact: jinfeng105@126.com / jinfeng105@gmail.com
