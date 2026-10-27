# Tessa

Tessa is a Telegram discussion assistant for ptchan. It listens
in one configured discussion chat and replies when a permitted person mentions
the bot or replies to one of its messages.

For a request that refers to a ptchan thread, Tessa can read a bounded,
sanitized context packet through ptchan-gateway before asking the configured
DeepSeek model for a response. It keeps a short conversation history per
Telegram topic in memory, then replies in that topic.

## Configure and run locally

Requirements: Go 1.26.10 or newer, a Telegram bot token, a DeepSeek API key, and
a ptchan-gateway integration with reading enabled.

Copy [config/example.toml](config/example.toml) to an untracked local file and
set the values for your environment. The example is the complete configuration
reference.

```bash
cp config/example.toml config/dev.toml
export CONFIG_FILE=config/dev.toml
export TELEGRAM_BOT_TOKEN=replace-me
export DEEPSEEK_API_KEY=replace-me
export PTCHAN_INTEGRATION_TESSA_SECRET=replace-me

go run ./cmd/tessa check-config
go run ./cmd/tessa run
```

The integration secret name follows the configured `ptchan.integration_name`:
`PTCHAN_INTEGRATION_<INTEGRATION_NAME>_SECRET`. For example, an integration
named `tessa-prod` uses `PTCHAN_INTEGRATION_TESSA_PROD_SECRET`.

`telegram.discussion_chat_id` selects the only chat Tessa will answer in.
Access is fail-closed: set `telegram.allowed_user_ids`, or explicitly set
`telegram.allow_all_users = true`. The `[chat]` section controls input bounds,
rate limits, retained history, and the maximum amount of ptchan context read
per request.

Tessa sends addressed message text, reply context, retained conversation
history, and any selected sanitized ptchan context to the model provider.
Telegram participants are replaced with temporary aliases before the model
call, but message content is not anonymized.

## Deploy and operate

Run exactly one Tessa instance for each Telegram bot token. Tessa uses Telegram
long polling, so multiple replicas would contend for the same updates.

Give the process a persistent SQLite location through `storage.sqlite_path` and
make the configured ptchan-gateway URL reachable from it. Keep the bot token,
model key, and integration secret in the deployment's secret store rather than
in TOML or source control. Tessa handles `SIGINT` and `SIGTERM` for clean
shutdown.

### Docker

The repository provides a small Docker workflow. It runs Tessa as an unprivileged
user with a read-only root filesystem, a temporary `/tmp`, and a fresh named
volume mounted at `/data`. Set `storage.sqlite_path = "data/tessa.db"` in the
selected configuration so the database lives on that volume.

```bash
cp .env.example .env.prod
cp config/example.toml config/prod.toml
# Replace example values in both files.

make docker-deploy TESSA_ENV=prod DOCKER_NETWORK=integration-net
make docker-logs TESSA_ENV=prod
```

`docker-deploy` builds the image, validates the selected configuration in the
container, then replaces only the named container. The SQLite volume remains.
The selected configuration is mounted read-only at `/etc/tessa/config.toml`.
Set `DOCKER_NETWORK` when Tessa must reach ptchan-gateway by its Docker service
name; the Makefile intentionally does not publish host ports.

The builder uses Go 1.26.10 and requires Debian's `ca-certificates` package
version `20250419+deb13u1` or newer. The scratch runtime copies that refreshed
bundle. These updates block the compromised emSign G3 intermediate chains,
including their AAA Certificate Services cross-signs (CVE-2026-111943).

### Health and metrics

When `runtime.http_addr` is set, Tessa serves these endpoints:

- `GET /healthz` — process health.
- `GET /readyz` — ready after the first successful Telegram poll; returns 503
  during identity startup, read outages/backoff, and worker shutdown.
- `GET /metrics` — Prometheus metrics.

`tessa check-health` requests `/healthz`; set `HEALTHCHECK_ADDR` when it is not
available on `127.0.0.1:9090`.

`/healthz` remains a liveness check during a Telegram outage. Use `/readyz` and
worker metrics to detect upstream failure without restarting a healthy process.
Readiness changes when a read finishes: an in-flight long poll can take up to the
40-second HTTP timeout to reveal an outage.

Tessa retries temporary `getMe` and `getUpdates` failures in process. Delays grow
exponentially from a 0.5–1 second interval to a 15–30 second interval, with jitter,
and reset after a successful read. Rate limits honor Telegram's
[`parameters.retry_after`](https://core.telegram.org/bots/api#responseparameters)
as a minimum delay, even above the 30-second cap. Shutdown cancels both requests
and waits. Authentication/access rejection, polling conflicts, invalid request
configuration, and protocol errors fail with a bounded diagnostic. For a polling
conflict, stop any other instance using the token and disable any webhook before
restarting.

Temporary polling outages retain the update cursor and process-local conversation
history (subject to its normal TTL). The cursor still advances only after an
update finishes processing. No automatic retries were added to `sendMessage`;
its existing rejected-Markdown fallback remains.

An outage logs its first failure, then at most one summary per minute, and one
recovery message. Read diagnostics contain only the method, bounded reason,
retry delay, and outage duration.

Telegram metrics:

- `tessa_telegram_poll_requests_total{result="success|error"}` — polling attempts,
  including retries; canceled requests are excluded.
- `tessa_telegram_read_requests_total{method="getMe|getUpdates",result="..."}` —
  read attempts, including identity startup and retries. Outcomes are `success`,
  `server`, `transport`, `rate_limited`, `credentials`, `conflict`, `configuration`,
  `permanent`, or `protocol`; canceled requests are excluded.
- `tessa_telegram_worker_ready` — 1 after a successful poll, 0 during startup,
  read outages, and shutdown.

After an approved Samantha application release, the operator should deploy only
Tessa through the infrastructure Compose assembly, retaining its configuration
and SQLite volume. Verify the scrape target, readiness and polling recovery, and
the restart count. A real Telegram reply check requires explicit operator
approval.

Metrics cover Telegram polling, admission decisions, reply delivery, in-memory
conversation count, ptchan/history/reply context use, and model completion and
token usage. Metric names are prefixed with `tessa_`. They use only bounded
operational labels; they never include message text, Telegram user or chat IDs,
thread IDs, prompts, completions, or secrets. Keep the metrics endpoint on an
appropriate internal network.

## Develop

Run the normal checks before handing off a change:

```bash
go mod tidy
go test ./...
go vet ./...
```

The code is intentionally small:

- `internal/app` loads configuration and wires the process, health checks, and metrics.
- `internal/chat` owns admission, memory, prompt construction, model calls, and replies.
- `internal/telegram` owns Telegram Bot API transport.
- `internal/chat/state` stores only the durable Telegram update cursor.

See [AGENTS.md](AGENTS.md) for the repository's Go style, package boundaries,
and safety rules.
