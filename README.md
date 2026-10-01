# TideLab

TideLab is a local-first Go project for reproducible crypto market experiments. Its planned scope is public market-data capture, limited-depth order-book reconstruction, hypothetical spot execution estimates, deterministic replay, and paper execution-plan comparisons. The core is designed to work without an exchange account, API key, or language model. It does not place trades.

This repository currently contains the **M0 bootstrap, M1 offline book, M2 execution estimator, and M3 public-feed recorder**. The available commands validate configuration, inspect synthetic book data, estimate hypothetical spot orders, capture Kraken public spot book frames, verify or recover local recordings, and run a loopback-only Fiber health endpoint. Deterministic replay, MCP tools, and the dashboard are planned work.

## Requirements

- Go 1.26.8 or later in a supported Go release series. The module pins the 1.26.8 toolchain and Fiber v3.5.0.
- Network access once to download Go modules. The commands below do not need exchange credentials.

## Run

```sh
go run ./cmd/tidelab version
go run ./cmd/tidelab config check
go run ./cmd/tidelab book inspect
go run ./cmd/tidelab estimate --side buy --base-qty 2.5 --fee-bps 10
go run ./cmd/tidelab record --symbol BTC/USD --duration 60s
go run ./cmd/tidelab serve
```

While the server is running, `curl http://127.0.0.1:8080/healthz` returns `{"status":"ok"}`. This checks only process health; it does not mean that market data is available.

Configuration is optional. Pass a JSON file with `-file` to `config check` or with `-config` to `serve`:

```json
{
  "data_dir": ".tidelab",
  "listen_addr": "127.0.0.1:8080"
}
```

Unknown fields, malformed JSON, files larger than 64 KiB, and non-loopback listen addresses are rejected. The data directory is reserved for future recordings; the bootstrap does not write market data.

`book inspect` reads `testdata/synthetic-market.json` from the repository root. Use `-fixture PATH` to inspect another file with the same schema. It applies a snapshot and ordered updates, then prints exact decimal strings, observed depth, data-quality state, and a deterministic SHA-256 book digest. This fixture is synthetic; its output explicitly reports `feed_live: false` and `checksum_valid: false`.

`estimate` reads `testdata/execution-book.json` by default. Supply `--fixture PATH` to use another synthetic book fixture. Set `--side buy|sell`, exactly one of `--base-qty DECIMAL` or `--quote-budget DECIMAL`, and an explicit `--fee-bps DECIMAL`. Quote budgets are available for buys and include estimated fees. `--max-slippage-bps DECIMAL` stops before a level outside the limit. Examples:

```sh
go run ./cmd/tidelab estimate --side sell --base-qty 2.5 --fee-bps 10
go run ./cmd/tidelab estimate --side buy --quote-budget 100 --fee-bps 10
go run ./cmd/tidelab estimate --side buy --base-qty 2.5 --fee-bps 10 --max-slippage-bps 50
```

The example buy of 2.5 TEST produces gross quote `251.5`, fee `0.2515`, and quote debit `251.7515`. Results include per-level fills, VWAP, slippage, remaining size or budget, stop reason, book digest, input fingerprint, and data-quality fields. Matching uses exact `math/big.Int` price ticks and quantity units; quote totals use an immutable `math/big.Int`-backed rational value. Budget sizing uses a bounded binary search over units. Financial JSON fields are decimal strings. Fees and notional rounding follow the reported simulation policy.

## Public market recording

`record` connects to Kraken's unauthenticated spot WebSocket v2 and subscribes to instrument metadata, heartbeats, and one L2 book. It validates the top-ten CRC32 checksum on snapshots and updates. Disconnects and checksum failures invalidate the current book; reconnects begin new segments and require a fresh snapshot. Use a supported depth of `10`, `25`, `100`, `500`, or `1000`.

```sh
go run ./cmd/tidelab record --symbol BTC/USD --depth 10 --duration 60s
go run ./cmd/tidelab record verify --session SESSION_ID
go run ./cmd/tidelab record recover --session SESSION_ID
```

Recordings are stored under `.tidelab/sessions/SESSION_ID/` by default; use `--data-dir PATH` with any of the commands to change the root. Each session has a manifest and ordered `frames.jsonl` envelopes containing the exact received frame bytes as base64. The manifest reports written and durable sequence positions separately, instrument history, segment boundaries, and a SHA-256 content hash after close. A single capture owns a data root at a time. Startup recovery and the explicit `recover` command label unfinished sessions `interrupted`; a truncated final line is saved as evidence before it is removed. Interior damage and hash mismatches are marked `corrupt`. `verify` does not change files.

The local defaults bound each frame to 1 MiB and the capture queue to 4096 frames and 32 MiB. The recorder fsyncs at least once per second or 1 MiB, and on close. A failed write, sync, or queue bound terminates the capture; it cannot become a complete recording. These are local capture limits, not Kraken protocol guarantees. This milestone records data but does not yet replay it into the estimator.

## Architecture

Features live under `internal/modules/<module>/`. Each module has these boundaries:

| Directory | Responsibility |
| --- | --- |
| `domain` | Entities, domain models, enums, and typed request/response data |
| `services` | Application use cases and business decisions |
| `infra` | Files, databases, exchange HTTP/WebSocket clients, and other external I/O |
| `routers` | Fiber HTTP routes that call services |
| `tools` | MCP tools that call the same services |

The `system`, `market`, and `execution` modules follow these boundaries. The Kraken adapter and recorder live under `market/infra`; capture orchestration lives under `market/services`. Unused router/tool directories are reserved for later HTTP and MCP surfaces. Entrypoint wiring lives in `cmd/tidelab`; synthetic fixture data is in `testdata/`. A small shared `internal/value` package provides immutable exact rational values. Domain logic stays independent of Fiber, MCP, and vendor SDKs; financial values use decimal strings at external boundaries.

## Development

```sh
go test ./...
go vet ./...
go build ./...
```

The local, ignored `docs/` directory holds the detailed product blueprint, project rules, decisions, and milestone evidence. `README.md` is the only root Markdown exception so GitHub can display this introduction. `docs/` is intentionally excluded from Git history.

## Roadmap

1. Exact decimal values and a validated offline order book — implemented.
2. Hypothetical execution estimates with explicit fees and observed-depth limits — implemented for synthetic fixtures.
3. Public Kraken feed capture and durable recordings — implemented; offline protocol and failure tests pass.
4. Deterministic replay, paper balances, and execution-plan comparison — next.
5. Shared CLI, Fiber HTTP API, MCP tools, and a local dashboard.

Results from observed order-book depth are simulations, not guarantees of real fills or profitability. No authenticated trading or custody features are planned for the first release.
