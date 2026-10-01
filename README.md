# TideLab

TideLab is a local-first Go project for reproducible crypto market experiments. Its planned scope is public market-data capture, limited-depth order-book reconstruction, hypothetical spot execution estimates, deterministic replay, and paper execution-plan comparisons. The core is designed to work without an exchange account, API key, or language model. It does not place trades.

This repository currently contains the **M0 bootstrap**, not the market engine. The available commands report the build version, validate local configuration, and run a loopback-only Fiber health endpoint. Market recording, estimation, replay, MCP tools, and the dashboard are planned work and are not available yet.

## Requirements

- Go 1.26.8 or later in a supported Go release series. The module pins the 1.26.8 toolchain and Fiber v3.5.0.
- Network access once to download Go modules. The commands below do not need exchange credentials.

## Run

```sh
go run ./cmd/tidelab version
go run ./cmd/tidelab config check
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

## Architecture

Features live under `internal/modules/<module>/`. Each module has these boundaries:

| Directory | Responsibility |
| --- | --- |
| `domain` | Entities, domain models, enums, and typed request/response data |
| `services` | Application use cases and business decisions |
| `infra` | Files, databases, exchange HTTP/WebSocket clients, and other external I/O |
| `routers` | Fiber HTTP routes that call services |
| `tools` | MCP tools that call the same services |

The initial `system` module demonstrates these boundaries. Its `tools` directory is reserved until MCP support is implemented. Entrypoint wiring lives in `cmd/tidelab`; synthetic fixture metadata is in `testdata/`. Domain logic must stay independent of Fiber, MCP, and vendor SDKs. Financial values will use exact arithmetic and decimal strings at external boundaries.

## Development

```sh
go test ./...
go vet ./...
go build ./...
```

The local, ignored `docs/` directory holds the detailed product blueprint, project rules, decisions, and milestone evidence. `README.md` is the only root Markdown exception so GitHub can display this introduction. `docs/` is intentionally excluded from Git history.

## Roadmap

1. Exact decimal values and a validated offline order book.
2. Hypothetical execution estimates with explicit fees and observed-depth limits.
3. Public Kraken feed capture and durable recordings.
4. Deterministic replay, paper balances, and execution-plan comparison.
5. Shared CLI, Fiber HTTP API, MCP tools, and a local dashboard.

Results from observed order-book depth are simulations, not guarantees of real fills or profitability. No authenticated trading or custody features are planned for the first release.
