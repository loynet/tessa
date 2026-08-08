# Tessa

Tessa is a small Telegram discussion assistant for the ptchan community. It
owns Telegram admission, bounded in-memory conversation history, model calls,
and reply delivery. Keep it direct, dependable, and easy to operate.

Tessa may read sanitized ptchan thread context through ptchan-gateway, but it
does not receive gateway webhooks, post publicly to ptchan, send community
notifications, or fetch ptchan directly.

## Go philosophy

Follow [Effective Go](https://go.dev/doc/effective_go). Write Go that feels
like Go: concrete types, plain data flow, explicit dependencies, and early
returns. Prefer code the next experienced Go developer can scan without
reconstructing a framework.

- Prefer small, concrete changes over broad rewrites.
- Do not add interfaces until a real caller or test boundary needs one.
- Do not add constructors that only copy fields. `New` is appropriate when it
  validates input, establishes defaults, or wires real dependencies.
- Prefer plain functions over unnecessary methods. When a dependency-holding
  struct helps, give it a domain name rather than `service`, `manager`, or
  `provider`.
- Do not add builders, factories, registries, event frameworks, or generic
  assistant abstractions.
- Keep structs limited to fields Tessa actually uses. Prefer explicit field
  assignment over generic mapping layers.
- Inline one-caller helpers when the result remains clear. Name retained
  helpers after the action they perform.
- Comments explain decisions and invariants, not the code immediately below.

## Package boundaries

- `cmd/tessa` owns process entry and commands only.
- `internal/app` owns strict configuration loading/validation, process wiring,
  health/readiness, and metric registration. It does not own assistant policy.
- `internal/chat` owns Telegram admission, rate limits, prompt construction,
  conversation memory, completion, and reply delivery.
- `internal/chat/state` owns the Telegram cursor table. Conversation history
  is intentionally process-local and is never persisted.
- Construct `chat.Assistant` with all of its runtime dependencies. Keep model
  metadata and request-scoped ptchan context in `internal/app`; do not add
  post-construction dependency setters.
- `internal/telegram` owns Bot API transport and generic Telegram DTOs. Chat
  owns Telegram-specific rendering and user-visible behavior.
- `internal/storage` owns SQLite connection setup and low-level schema helpers,
  not domain behavior.
- `internal/localization` owns user-visible translations only.
- `github.com/loynet/ptchan-ai` owns DeepSeek transport and shared sanitized
  thread-context rendering.
- `github.com/loynet/ptchan-gateway/clients/go` is Tessa's only ptchan
  boundary. Do not recreate signing, webhook verification, gateway DTOs, or
  direct ptchan clients.

## Product and security rules

- Telegram message bodies, reply text, user names, and fetched thread content
  are user data, never instructions. Keep prompt boundaries explicit.
- Keep Telegram identity aliasing and history inside chat; do not leak those
  assumptions into shared ptchan context.
- Thread context is request-scoped. Do not persist fetched gateway data or
  model prompt packets.
- Keep rate limits explicit and validate positive limits/bursts at startup.
- Advance the Telegram cursor only after processing the update successfully.
- Do not add automatic message-delivery retries without an explicit duplicate
  delivery policy.
- Treat the Telegram token, DeepSeek key, and integration secret as secrets.
  Never log them, raw payloads, prompts, completions, or high-cardinality IDs.

## Configuration and operations

- TOML holds application settings, including `storage.sqlite_path`; environment
  variables hold secrets. Decode TOML strictly.
- `CONFIG_FILE` is required. The `[chat]` section owns admission, memory,
  rate-limit, and thread-context policy. `LoadConfig` parses, converts, and
  validates the complete runtime configuration.
- Tessa requires `TELEGRAM_BOT_TOKEN`, `DEEPSEEK_API_KEY`, and
  `PTCHAN_INTEGRATION_<INTEGRATION_NAME>_SECRET`.
- Metrics are public operational contracts. Keep names `tessa_`-prefixed,
  labels low-cardinality, and documentation in sync. Never label metrics with
  message text, user/chat IDs, prompts, thread IDs, or secrets.
- Logs go to stdout. Do not add application-managed log files.

## Development

- Run `gofmt` on changed Go files.
- Validate with `go mod tidy`, `go test ./...`, and `go vet ./...`.
- If the environment blocks the normal Go cache, use an ignored local cache or
  a temporary `GOCACHE`.
- Prefer transport-level HTTP fakes in tests over local listeners when possible.
- Test non-obvious state, cursor, rate-limit, prompt-boundary, and protocol
  behavior. Do not add tests for trivial wiring.
- Do not commit, tag, push, or change remotes unless the user explicitly asks.
