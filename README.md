# Tessa

Tessa is a Telegram discussion assistant for ptchan. It listens
in one configured discussion chat and replies when a permitted person mentions
the bot or replies to one of its messages.

For a request that refers to a ptchan thread, Tessa can read a bounded,
sanitized context packet through ptchan-gateway before asking the configured
DeepSeek model for a response. It keeps a short conversation history per
Telegram topic in memory, then replies in that topic.

## Configure and run locally

Requirements: Go 1.25 or newer, a Telegram bot token, a DeepSeek API key, and
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

### Health and metrics

When `runtime.http_addr` is set, Tessa serves these endpoints:

- `GET /healthz` — process health.
- `GET /readyz` — ready after Tessa has finished its startup wiring.
- `GET /metrics` — Prometheus metrics.

`tessa check-health` requests `/healthz`; set `HEALTHCHECK_ADDR` when it is not
available on `127.0.0.1:9090`.

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
