<p align="center">
  <h1 align="center">CNIPS CLI</h1>
  <p align="center">
    <strong>The official command-line interface for the CNIPS Integration Platform</strong>
  </p>
  <p align="center">
    <a href="#installation">Installation</a> •
    <a href="#quick-start">Quick Start</a> •
    <a href="#commands">Commands</a> •
    <a href="#examples">Examples</a> •
    <a href="#configuration">Configuration</a>
  </p>
</p>

---

The CNIPS CLI enables local development, testing, and deployment of integration pipelines, components, and functions. Build and run pipelines locally with hot-reload, sync changes with remote workspaces, and publish components to the marketplace — all from your terminal.

## Features

- **Local Pipeline Execution** — Run pipelines locally with native components (JSONata transformations, decisions, switches, loops, approvals)
- **Hot-Reload Development** — `cnips fn dev` watches source files and automatically rebuilds on changes
- **Multi-Language Support** — JavaScript/TypeScript, Python, and Go components and functions
- **Git-like Workflow** — Pull, push, diff, status, stash, and rebase operations for workspace synchronization
- **Validation & Formatting** — Lint project files, detect secret leaks, and enforce canonical YAML formatting
- **Marketplace Publishing** — Submit components directly to the CNIPS marketplace

## Installation

### Using `go install`

Requires [Go 1.25+](https://go.dev/dl/).

```bash
go install github.com/cnips/cli/cmd/cnips@latest
```

### From Source

```bash
# Clone the repository
git clone https://github.com/cnips/cli.git
cd cli

# Install globally
go install ./cmd/cnips

# Or build to a local binary
go build -o bin/cnips ./cmd/cnips
```

### Verify Installation

```bash
cnips --help
```

## Quick Start

### 1. Initialize a New Project

```bash
cnips init my-integrations
cd my-integrations
```

This creates the standard project structure:

```
my-integrations/
├── cnips.yaml              # Project manifest
├── cnips.lock              # Dependency lockfile
├── pipelines/              # Pipeline definitions
├── components/             # Local components
├── functions/              # FaaS functions
├── sources/                # Source components (extractors)
├── destinations/           # Destination components
├── transformations/        # Transformation components
├── approvals/              # Approval components
├── switches/               # Switch components
├── decisions/              # Decision components
├── environments/           # Environment configurations
│   └── dev.yaml
└── .cnips/                 # Runtime cache (gitignored)
```

### 2. Authenticate with CNIPS

```bash
# Browser-based OIDC login
cnips login --base-url https://your-cnips-instance.com

# Or with a token
cnips login --token "$CNIPS_TOKEN" --workspace default
```

### 3. Pull Existing Workspace

```bash
cnips pull
```

### 4. Create a Component

```bash
# Create a transformation in JavaScript
cnips add normalize-order --type transformation --language javascript

# Create a Go function with Fiber framework
cnips add webhook-handler --type function --language go --go-framework fiber

# Create a Python source extractor
cnips add api-fetcher --type source --language python
```

### 5. Run Locally

```bash
# Run a pipeline with JSON payload
cnips run my-pipeline --payload '{"orderId": "12345"}'

# Run with payload from file
cnips run my-pipeline --payload-file ./test-data/order.json

# Run with auto-approval for approval steps
cnips run my-pipeline --auto-approve --verbose
```

### 6. Push Changes

```bash
# Validate before pushing
cnips validate

# Push to workspace
cnips push
```

## Commands

### Project Management

| Command | Description |
|---------|-------------|
| `cnips init [name]` | Scaffold a new CNIPS project |
| `cnips add <name>` | Create a new component or function |
| `cnips rename <type> <old> <new>` | Rename a component, function, or pipeline |
| `cnips validate` | Validate project files before push |
| `cnips fmt [path]` | Format CNIPS YAML files canonically |
| `cnips build [name]` | Build components and functions |

### Authentication

| Command | Description |
|---------|-------------|
| `cnips login` | Authenticate with mgmt-srv (browser OIDC or token) |

### Workspace Sync

| Command | Description |
|---------|-------------|
| `cnips pull` | Pull workspace artifacts to local files |
| `cnips push` | Apply local files to a workspace |
| `cnips diff` | Show differences between local and remote |
| `cnips status` | Show local and remote changes against base |
| `cnips stash [push\|list\|apply\|pop]` | Temporarily save local changes |
| `cnips rebase` | Replay local work on top of a base workspace |

### Local Execution

| Command | Description |
|---------|-------------|
| `cnips run <pipeline>` | Execute a pipeline locally |
| `cnips fn dev <function>` | Run a function with hot-reload |
| `cnips fun <function>` | Alias for `fn dev` |
| `cnips trace <run-id>` | Show a pipeline run trace |

### Publishing

| Command | Description |
|---------|-------------|
| `cnips publish <type> <name>` | Submit a component to the marketplace |

## Command Reference

### `cnips init`

```bash
cnips init [project-name]
```

Creates a new CNIPS project with the standard folder structure. If no name is provided, defaults to `my-integrations`.

### `cnips add`

```bash
cnips add <component-name> --type <type> --language <lang> [--go-framework <framework>]
```

**Flags:**
- `--type, -t` — Component type: `source`, `destination`, `transformation`, `approval`, `switch`, `decision`, `component`, or `function`
- `--language, -l` — Language: `javascript`, `python`, or `go`
- `--go-framework` — Go function framework: `http` (default) or `fiber`
- `--description, -d` — Description for the component manifest

### `cnips login`

```bash
cnips login [--base-url <url>] [--token <token>] [--workspace <id>]
```

**Flags:**
- `--base-url` — CNIPS origin URL for browser login
- `--api-url` — Direct mgmt-srv URL (default: `http://localhost:8090`)
- `--token` — Bearer token for direct authentication
- `--workspace` — Workspace to select after login
- `--tenant-key` — Tenant key header value
- `--profile` — Local auth profile name (default: `default`)
- `--force` — Force fresh login, ignoring cached tokens
- `--skip-verify` — Save credentials without verifying against mgmt-srv

### `cnips pull`

```bash
cnips pull [--workspace <id>] [--dry-run] [--all-workspaces]
```

Fetches pipelines, components, functions, global variables, and configurations from a workspace and writes them as canonical YAML files.

**Flags:**
- `--workspace` — Workspace ID to pull from (default: `default`)
- `--dry-run` — Print summary without writing files
- `--all-workspaces` — Pull from all accessible workspaces
- `--skip-versions` — Skip component version lookups
- `--version-workers` — Concurrent version lookup workers (default: `32`)

### `cnips push`

```bash
cnips push [--workspace <id>] [--dry-run]
```

Applies local canonical files to a target workspace. Creates or updates components, functions, and pipelines. Waits for server-side builds to complete.

**Flags:**
- `--workspace` — Workspace ID to push to (default: `default`)
- `--dry-run` — Print plan without applying changes

### `cnips run`

```bash
cnips run <pipeline> [--payload <json>] [--payload-file <path>] [--env <env>]
```

Executes a pipeline locally. Supports native components:
- `native/transformation@1` — JSONata transformations
- `native/decision@1` — Boolean conditions with branching
- `native/switch@1` — Multi-way routing
- `native/loop@1` — Array iteration (sequential or concurrent)
- `native/approval@1` — Interactive terminal prompts
- `native/http-source@1` — Outbound HTTP calls

**Flags:**
- `--payload, -p` — JSON string payload
- `--payload-file, -f` — Path to JSON payload file
- `--env, -e` — Environment name (default: `dev`)
- `--from` — Start from a specific step ID
- `--trace` — Previous trace ID for `--from` execution
- `--auto-approve` — Auto-approve approval steps
- `--auto-reject` — Auto-reject approval steps
- `--verbose, -v` — Print each step as it executes

### `cnips fn dev`

```bash
cnips fn dev <function-name>
```

Starts a local HTTP server for function development with file watching. Automatically rebuilds and restarts on source changes.

**Endpoints:**
- `GET /ping` — Health check
- `POST /execute` — Execute with JSON body

**Supported Runtimes:**
- JavaScript/TypeScript (via Bun or Node)
- Go
- Python 3

### `cnips validate`

```bash
cnips validate [--workspace <id>]
```

Validates project files before push:
- YAML parsing for all artifact types
- Pipeline graph integrity and step references
- Component/function reference resolution
- Secret leak detection (literal credentials in YAML)
- Lock file integrity verification

### `cnips publish`

```bash
cnips publish <type> <name> [--marketplace-version <version>]
```

Submits a pushed component to the CNIPS marketplace. Requires a successful platform build.

**Types:** `transformation`, `destination`, `switch`, `approval`, `decision`

**Flags:**
- `--component-version` — Platform version to publish (default: latest)
- `--marketplace-version` — Semantic version for marketplace
- `--description` — Marketplace description
- `--logo-url` — Marketplace logo URL
- `--wait` — Wait for upload completion (default: `true`)

## Configuration

### Project Manifest (`cnips.yaml`)

```yaml
apiVersion: cnips.io/v1
kind: Project
metadata:
  name: my-integrations
spec:
  runtime: "2026.8"
  registry: https://marketplace.cnips.io
```

### Environment Configuration (`environments/dev.yaml`)

```yaml
apiVersion: cnips.io/v1
kind: Environment
metadata:
  name: dev
spec:
  target:
    workspace: local
  variables:
    region: local
  secrets:
    api_key:
      ref: platform://secrets/api-key
```

### Auth Profiles

Authentication profiles are stored at `~/.cnips/config.yaml`:

```yaml
currentProfile: default
profiles:
  default:
    apiURL: http://localhost:8090
    workspaceID: default
    tenantKey: my-tenant
```

## Examples

### Creating a JSONata Transformation

```bash
cnips add normalize-order --type transformation --language javascript
```

Edit `transformations/normalize-order/handler.js`:

```javascript
// JSONata expression for order normalization
export default `{
  "orderId": id,
  "customer": customer.name,
  "total": $sum(items.price * items.quantity),
  "items": items.{
    "sku": productId,
    "qty": quantity,
    "price": price
  }
}`;
```

### Creating a Pipeline

Create `pipelines/order-sync/pipeline.yaml`:

```yaml
apiVersion: cnips.io/v1
kind: Pipeline
metadata:
  name: order-sync
spec:
  steps:
    - id: fetch-orders
      uses: source/api-fetcher@latest
      next: normalize

    - id: normalize
      uses: transformation/normalize-order@latest
      next: route

    - id: route
      uses: native/switch@1
      with:
        expression: "$.total > 1000"
      cases:
        "true": high-value
        "false": standard

    - id: high-value
      uses: destination/priority-queue@latest

    - id: standard
      uses: destination/standard-queue@latest
```

### Running with Different Environments

```bash
# Development
cnips run order-sync --env dev --payload-file ./test/order.json

# Staging (requires environments/staging.yaml)
cnips run order-sync --env staging --payload-file ./test/order.json
```

### Function Development Workflow

```bash
# Create a Go function
cnips add webhook-handler --type function --language go

# Start development server
cnips fn dev webhook-handler

# In another terminal, test the function
curl -X POST http://localhost:8080/execute \
  -H "Content-Type: application/json" \
  -d '{"event": "order.created", "data": {"id": "123"}}'
```

### Publishing to Marketplace

```bash
# Ensure component is pushed and built
cnips push

# Publish to marketplace
cnips publish transformation normalize-order \
  --marketplace-version 1.0.0 \
  --description "Normalizes order data to canonical format"
```

## Development

### Running Tests

```bash
make test
# or
go test ./...
```

### Building

```bash
make build
# or
go build -o bin/cnips ./cmd/cnips
```

### Project Structure

```
cmd/cnips/              CLI binary entrypoint
internal/
├── cli/                Cobra commands and orchestration
├── artifact/           Local YAML artifact models and parsers
├── auth/               CLI profile and token handling
├── builder/            Component and function build support
├── platform/           mgmt-srv API client and DTOs
├── runtime/            Local pipeline runtime engine
├── serializer/         Remote-to-local artifact serializers
├── fnrunner/           Function development server
└── executionlog/       Pipeline execution logging
```

## Requirements

- **Go 1.25+** for building from source
- **Bun** (recommended) or **Node.js** for JavaScript/TypeScript functions
- **Python 3.12+** for Python functions
- **mgmt-srv** running locally or accessible remotely for sync operations

## Contributing

Contributions are welcome! Please ensure:

1. All tests pass (`go test ./...`)
2. Code is formatted (`go fmt ./...`)
3. New commands include help text and examples

## License

This project is licensed under the GNU General Public License v3.0 — see the [LICENSE](LICENSE) file for details.
