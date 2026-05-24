# gorouter

`gorouter` is an Apollo Federation v2 gateway implemented in Go. It serves as both a runnable HTTP server (a drop-in for Apollo Router) and an importable library whose `federation` package provides query planning and execution for use in other Go programs.

## Two modes of use

### 1. CLI gateway server

```sh
go install github.com/StevenACoffman/gorouter@latest

gorouter run --supergraph supergraph.graphql --config router.yaml
```

Starts two HTTP servers:
- GraphQL endpoint (default `127.0.0.1:4000`) — accepts `GET` and `POST` GraphQL requests
- Health check endpoint (default `127.0.0.1:8088/health`)

### 2. Importable library

```go
import "github.com/StevenACoffman/gorouter/federation"

sg, err := federation.ParseSchema(sdl)
plan, err := federation.BuildPlan(sg, query, operationName)
data, errs, err := federation.Execute(ctx, plan, variables, httpClient)
```

Used by the `defederator` code generator to compile query plans into generated Go clients at generation time.

## CLI commands

```
gorouter run [FLAGS]          Start the gateway HTTP server
gorouter version              Print version information
gorouter config schema        Print a JSON Schema for the config file
gorouter config validate      Validate a config file
```

### `run` flags

| Flag | Env var | Default | Description |
|---|---|---|---|
| `-c`, `--config` | `APOLLO_ROUTER_CONFIG` | _(none)_ | Path to YAML config file |
| `-s`, `--supergraph` | `APOLLO_ROUTER_SUPERGRAPH_PATH` | _(none)_ | Path to supergraph SDL file |
| `--listen` | `APOLLO_ROUTER_LISTEN` | _(from config)_ | Override listen address |
| `--dev` | `APOLLO_ROUTER_DEV` | `false` | Development mode |
| `--hot-reload` | `APOLLO_ROUTER_HOT_RELOAD` | `false` | Reload config and schema on file changes |
| `-l`, `--log` | `APOLLO_ROUTER_LOG` | `info` | Log level: `off error warn info debug trace` |

Every flag can be set via its `APOLLO_ROUTER_`-prefixed environment variable. Command-line flags take precedence over environment variables.

## Config file (YAML)

```yaml
supergraph:
  listen: "127.0.0.1:4000"    # GraphQL endpoint address
  path: "/"                    # URL path for the GraphQL endpoint
  introspection: false
  defer_support: true
  connection_shutdown_timeout: "60s"

health_check:
  listen: "127.0.0.1:8088"
  enabled: true
  path: "/health"

homepage:
  enabled: true    # mutually exclusive with sandbox

sandbox:
  enabled: false   # Apollo Sandbox explorer UI; mutually exclusive with homepage

cors:
  allow_credentials: false
  allow_methods: [GET, POST, OPTIONS]
  expose_headers: [Content-Length, Content-Type]
  allow_headers: []
  allow_origins: []

apq:
  enabled: true    # Automatic Persisted Queries

batching:
  enabled: false
  mode: "batch_http_link"
```

Print the full JSON Schema with `gorouter config schema`. Validate an existing file with `gorouter config validate -c router.yaml`.

## `federation` package

The `federation` package is the core library. It is imported by defederator's code generator and can be imported by any Go program that needs federation-aware query execution.

```go
import "github.com/StevenACoffman/gorouter/federation"
```

### Key types and functions

```go
// ParseSchema parses a Federation v2 supergraph SDL and extracts routing information.
func ParseSchema(sdl string) (*Supergraph, error)

// BuildPlan constructs an execution plan for a GraphQL operation.
// operationName may be "" for documents with a single operation.
func BuildPlan(sg *Supergraph, query, operationName string) (*Plan, error)

// Execute runs a plan against the subgraphs: parallel initial fetches,
// sequential entity fetches, then merge. Returns GraphQL-level errors separately.
func Execute(ctx context.Context, plan *Plan, vars map[string]any, client *http.Client) (map[string]any, []GraphQLError, error)

// Handler returns an http.Handler that accepts GraphQL-over-HTTP requests
// and executes them against the supergraph.
func Handler(sg *Supergraph, client *http.Client) http.Handler

// SubgraphURLs returns the URL map extracted from the supergraph SDL.
// Useful for seeding URL overrides from environment variables.
func SubgraphURLs(sg *Supergraph) map[string]string
```

### URL overrides

Subgraph URLs are embedded in the supergraph SDL but can be overridden at runtime (for example, to point to local services or per-environment URLs):

```go
sg, _ := federation.ParseSchema(sdl)
sg = sg.WithURLOverrides(map[string]string{
    "PRODUCTS": os.Getenv("PRODUCTS_URL"),
    "USERS":    os.Getenv("USERS_URL"),
})
```

### Plan inspection

```go
type Plan struct {
    Fetches       []*FetchSpec       // initial parallel queries, one per subgraph
    EntityFetches []*EntityFetchSpec // sequential entity resolution steps
    Projection    []*FieldProjection // user-requested field tree (strips planner-added fields)
}
```

`BuildPlan` is deterministic for a given (SDL, query, operationName) triple and is safe to call concurrently. Cache plans if you call the same operation repeatedly.

## `tools/recorder`

`tools/recorder` is a transparent recording proxy that sits between Apollo Router and a subgraph and captures each request/response pair to JSON files. Use it to build golden fixture files for test suites.

```sh
go run ./tools/recorder \
  --upstream http://localhost:4002 \
  --listen :5002 \
  --name USERS \
  --out /tmp/golden-record/proxy
```

| Flag | Description |
|---|---|
| `--upstream` | The real subgraph URL to forward traffic to (required) |
| `--listen` | Address to listen on for incoming Apollo Router traffic |
| `--name` | Subgraph enum name used as a filename prefix (e.g. `USERS`) |
| `--out` | Output directory for captured JSON files |

Run one recorder instance per subgraph. Each captured call is written as `<NAME>_<N>.json` containing both the request and response, making the files suitable for scripted test servers.

## Package layout

```
gorouter/
├── main.go                   # Entry point; signal handling, exits
├── cmd/
│   ├── cmd.go                # Command dispatcher (registers all subcommands)
│   ├── root/                 # Root flags and shared config
│   ├── run/                  # "gorouter run" subcommand
│   ├── version/              # "gorouter version" subcommand
│   └── config/               # "gorouter config" command group
│       ├── schema/           # "gorouter config schema" (prints JSON Schema)
│       └── validate/         # "gorouter config validate"
├── federation/               # Core library: parsing, planning, execution
│   ├── supergraph.go         # Supergraph type, ParseSchema, WithURLOverrides
│   ├── plan.go               # Plan, FetchSpec, EntityFetchSpec, FieldProjection
│   ├── plan_spec.go          # PlanSpec: serializable enum-keyed plan format
│   ├── execute.go            # Execute: parallel fetch + entity resolution + merge
│   └── handler.go            # Handler: GraphQL-over-HTTP entry point
├── internal/
│   └── config/               # Config type, YAML parsing, SchemaJSON
└── tools/
    └── recorder/             # Transparent recording proxy for fixture capture
```

## Dependencies

| Dependency | Role |
|---|---|
| `github.com/peterbourgon/ff/v4` | CLI flag parsing and subcommand routing |
| `gopkg.in/yaml.v3` | Config file YAML parsing |
| `github.com/vektah/gqlparser/v2` | GraphQL SDL and operation parsing |

## Relationship to defederator

`defederator` uses `gorouter/federation` at code-generation time to compile query plans for each operation in your `.graphql` files. The resolved plans (with subgraph URLs baked in) are embedded as JSON string constants in the generated client. The generated client then uses a bundled copy of the execution engine (`federation_exec.go`) with no import of `gorouter` at runtime — making the generated client depend only on the Go standard library.

## Workspace setup

```
agent-orange/
├── go.work             # use ./gorouter ./defederator ./gqlgenc
├── gorouter/           # this module
├── defederator/
└── gqlgenc/
```
