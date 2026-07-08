# caravan

[![CI](https://github.com/evansmurithi/caravan/actions/workflows/ci.yml/badge.svg)](https://github.com/evansmurithi/caravan/actions/workflows/ci.yml)
[![Release](https://github.com/evansmurithi/caravan/actions/workflows/release.yml/badge.svg)](https://github.com/evansmurithi/caravan/actions/workflows/release.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/evansmurithi/caravan.svg)](https://pkg.go.dev/github.com/evansmurithi/caravan)

GitOps for [HashiCorp Nomad](https://www.nomadproject.io/).

caravan continuously reconciles a Nomad cluster with the job specifications
stored in a Git repository. It polls the repo, renders your `.nomad`/`.hcl` job
files, computes a diff against the live cluster using Nomad's native plan API,
and applies changes so the cluster always matches Git.

## How it works

```
        ┌─────────────┐      poll/pull      ┌──────────────────────────────┐
        │  Git repo   │ ◀────────────────── │            caravan           │
        │ *.nomad.hcl │                     │  ┌────────┐   ┌────────────┐ │
        └─────────────┘                     │  │  git   │──▶│  discover  │ │
                                            │  │ source │   │  + parse   │ │
                                            │  └────────┘   └─────┬──────┘ │
                                            │                     ▼        │
                                            │  ┌────────┐   ┌────────────┐ │
        ┌─────────────┐   plan / register   │  │ nomad  │◀──│ reconcile  │ │
        │Nomad cluster│ ◀────────────────── │  │ client │   │   loop     │ │
        └─────────────┘                     │  └────────┘   └────────────┘ │
                                            └──────────────────────────────┘
```

Each reconcile pass:

1. **Sync** — clone or fast-forward the tracked branch of the Git repo.
2. **Discover** — walk the configured sub-path for `.nomad` / `.hcl` job files.
3. **Parse** — parse each file locally with Nomad's `jobspec2` package (the same
   parser `nomad job run` uses), with filesystem functions like `file()` enabled
   and resolved relative to the base directory (see below). Each job is stamped
   with a `caravan.gitops/managed-by` meta label.
4. **Plan** — call Nomad's plan endpoint to get a structured diff.
5. **Apply** — in `sync` mode, register jobs that have pending changes. In
   `plan` mode, only report the diff (dry-run).
6. **Prune** *(optional)* — stop jobs previously managed by caravan that are no
   longer present in Git.

HCL is parsed client-side (not via the server's `/v1/jobs/parse` endpoint) so
that filesystem functions such as `file(...)` — commonly used to inline config
into `template` blocks — work exactly as they do with the `nomad job run` CLI.

## Commands

```
caravan run       Continuously reconcile the cluster with Git (default)
caravan diff      Show what would change without applying, then exit
caravan version   Print the version
```

Flags are shared across commands (`caravan <command> --help`).

## Quick start

Build the binary:

```bash
make build
```

Preview what would change, in a `nomad plan`-style tree, without touching the
cluster:

```bash
caravan diff \
  --git-url https://github.com/you/nomad-jobs.git \
  --git-path jobs
```

```
caravan diff @ 41793588

~ Job: "prometheus"
  ~ Task Group: "prometheus"
    ~ Task: "prometheus"
      ~ Template {
        ~ EmbeddedTmpl: "...old..." => "...new..."

- Job "old-batch" (default) would be pruned: managed by caravan but not in Git
```

The `diff` command exits non-zero if it cannot sync Git or plan a job, so it can
gate a CI job. Orphan/prune entries are only shown when `--prune` is set.

Run a one-shot dry-run against a local Nomad agent:

```bash
export NOMAD_ADDR=http://127.0.0.1:4646

./bin/caravan \
  --git-url https://github.com/you/nomad-jobs.git \
  --git-branch main \
  --git-path jobs \
  --mode plan \
  --once
```

Run continuously and actually apply changes:

```bash
./bin/caravan \
  --git-url https://github.com/you/nomad-jobs.git \
  --git-path jobs \
  --mode sync \
  --prune \
  --sync-interval 30s
```

## Configuration

Configuration is layered with [Viper](https://github.com/spf13/viper). For any
setting, the precedence is:

1. **Command-line flag** (e.g. `--git-url`)
2. **Environment variable** (e.g. `CARAVAN_GIT_URL`; Nomad settings also accept
   the standard `NOMAD_ADDR`, `NOMAD_TOKEN`, `NOMAD_REGION`, `NOMAD_NAMESPACE`)
3. **Config file** (`--config caravan.yaml`, supports YAML/TOML/JSON)
4. **Built-in default**

Env vars are the `CARAVAN_`-prefixed, upper-snake-case form of each flag
(`--git-ssh-passphrase` → `CARAVAN_GIT_SSH_PASSPHRASE`). A config file uses the
flag name as the key:

```yaml
# caravan.yaml
git-url: https://github.com/you/nomad-jobs.git
git-path: nomad
mode: sync
prune: true
sync-interval: 30s
```

| Flag | Env var | Default | Description |
| --- | --- | --- | --- |
| `--config` | — | — | Path to a config file (YAML/TOML/JSON) |
| `--git-url` | `CARAVAN_GIT_URL` | — (required) | Git repository URL |
| `--git-branch` | `CARAVAN_GIT_BRANCH` | `main` | Branch to track |
| `--git-path` | `CARAVAN_GIT_PATH` | `.` | Sub-path to scan for job specs |
| `--git-token` | `CARAVAN_GIT_TOKEN` | — | Access token for HTTP(S) auth |
| `--git-ssh-key` | `CARAVAN_GIT_SSH_KEY` | — | Path to SSH private key |
| `--git-ssh-passphrase` | `CARAVAN_GIT_SSH_PASSPHRASE` | — | Passphrase for the SSH private key |
| `--file-base-dir` | `CARAVAN_FILE_BASE_DIR` | `--git-path` | Directory (relative to repo root) that HCL `file()` calls resolve against |
| `--nomad-addr` | `CARAVAN_NOMAD_ADDR` / `NOMAD_ADDR` | `http://127.0.0.1:4646` | Nomad API address |
| `--nomad-token` | `CARAVAN_NOMAD_TOKEN` / `NOMAD_TOKEN` | — | Nomad ACL token |
| `--nomad-region` | `CARAVAN_NOMAD_REGION` / `NOMAD_REGION` | — | Nomad region |
| `--nomad-namespace` | `CARAVAN_NOMAD_NAMESPACE` / `NOMAD_NAMESPACE` | `default` | Namespace for managed jobs |
| `--mode` | `CARAVAN_MODE` | `sync` | `sync` (apply) or `plan` (dry-run) |
| `--prune` | `CARAVAN_PRUNE` | `false` | Stop managed jobs removed from Git |
| `--sync-interval` | `CARAVAN_SYNC_INTERVAL` | `1m` | Reconcile frequency |
| `--once` | `CARAVAN_ONCE` | `false` | Run one pass and exit |
| `--log-level` | `CARAVAN_LOG_LEVEL` | `info` | `debug`/`info`/`warn`/`error` |
| `--log-format` | `CARAVAN_LOG_FORMAT` | `text` | `text` or `json` |
| `--health-addr` | `CARAVAN_HEALTH_ADDR` | `:8080` | Health server address (empty to disable) |

## Job repository layout

Point `--git-path` at a directory of Nomad job files. Any file ending in
`.nomad` or `.hcl` (recursively, excluding hidden/`.git` dirs) is treated as a
job spec. Each file must define exactly one job with a unique ID.

```
nomad-jobs/
└── jobs/
    ├── hello.nomad.hcl
    ├── api.nomad.hcl
    └── batch/
        └── nightly.nomad.hcl
```

See [`examples/jobs/hello.nomad.hcl`](examples/jobs/hello.nomad.hcl) for a
starting point.

### External config files

Jobs frequently pull in external configuration with HCL filesystem functions
(`file`, `filebase64`, `fileset`, `fileexists`, ...). caravan enables these and
resolves their paths against a single **base directory** — the analog of the
working directory you'd `cd` into before running `nomad job run`. By default the
base directory is the `--git-path` scan root; override it with `--file-base-dir`.

This matters because the `nomad job run` CLI resolves `file()` relative to your
current shell directory, *not* the job file's location. So if your jobs are
written to be deployed from a `nomad/` directory:

```
nomad/                              <- base directory (cd here, then nomad job run)
├── prometheus/
│   ├── prometheus.nomad            # data = file("prometheus/alerts.yaml")
│   └── alerts.yaml
└── alloy/
    ├── alloy.nomad                 # data = file("alloy/configs/agent.alloy")
    └── configs/
        └── agent.alloy
```

then configure caravan so `file()` resolves against `nomad/`:

- **Manage everything under `nomad/`:** `--git-path nomad` (base dir defaults to
  the scan root, so no extra flag needed), or
- **Manage a single app but keep the same paths:** `--git-path nomad/alloy
  --file-base-dir nomad`.

The referenced content is inlined into the job at parse time, so it travels with
the job spec to Nomad. A side benefit: changing `alerts.yaml` alone changes the
rendered job, so caravan's next plan detects a diff and re-applies — config-only
changes are reconciled just like changes to the job HCL itself.

Only `.nomad` and `.hcl` files are treated as job specs; config files such as
`.yaml`, `.alloy`, `.json`, etc. are ignored by the job scanner. Avoid using the
`.hcl` extension for non-job config files, or they'll be parsed as jobs.

## Pruning

With `--prune`, caravan stops any job carrying its `caravan.gitops/managed-by`
meta label that is no longer defined in Git. Jobs created outside of caravan are
never touched. In `plan` mode, orphans are reported but not stopped.

## Health & status

When running continuously, caravan exposes:

- `GET /healthz` — liveness (always `200` once serving)
- `GET /readyz` — readiness (`200` after the first reconcile)
- `GET /status` — JSON summary of the last reconcile (commit, applied/pending/pruned jobs, error)

## Deploying to Nomad

Build and push the image, then run caravan as a job in the cluster it manages:

```bash
make docker IMAGE=ghcr.io/you/caravan:latest
docker push ghcr.io/you/caravan:latest

nomad job run \
  -var "git_url=https://github.com/you/nomad-jobs.git" \
  -var "git_path=jobs" \
  -var "image=ghcr.io/you/caravan:latest" \
  deploy/caravan.nomad.hcl
```

See [`deploy/caravan.nomad.hcl`](deploy/caravan.nomad.hcl) for the full spec,
including where to wire in a Nomad ACL token via Nomad Variables or Vault.

## Development

```bash
make check   # fmt-check, vet, lint, test (mirrors CI)
make build
```

## CI/CD

GitHub Actions live in [.github/workflows](.github/workflows):

- **CI** (`ci.yml`) — on every push/PR: `gofmt` check, `go vet`, `golangci-lint`,
  race-enabled tests with coverage, a cross-platform build matrix
  (linux/darwin/windows x amd64/arm64), and `govulncheck`.
- **Docker** (`docker.yml`) — on pushes to `main` and version tags: builds and
  pushes multi-arch (amd64/arm64) images to GHCR
  (`ghcr.io/evansmurithi/caravan`), tagged with `latest`, branch, semver, and
  commit SHA.
- **Release** (`release.yml`) — on `v*` tags: [GoReleaser](https://goreleaser.com)
  builds cross-platform archives, checksums, and a GitHub Release with an
  autogenerated changelog (config in [.goreleaser.yaml](.goreleaser.yaml)).
- **CodeQL** (`codeql.yml`) — scheduled + PR security analysis.
- **Renovate** ([.github/renovate.json](.github/renovate.json)) — weekly updates
  for Go modules, GitHub Actions, and the Docker base image. All GitHub Actions
  are pinned to commit SHAs (with a version comment), and Renovate keeps both the
  digest and comment up to date.

Cut a release by pushing a semver tag:

```bash
git tag v0.1.0
git push origin v0.1.0
```

## Roadmap ideas

- Webhook-triggered reconciliation (in addition to polling)
- Prometheus metrics on `/metrics`
- Multi-source / per-directory namespace mapping
- Health assessment of deployments after apply (rollback on failure)
- Nomad Pack and Levant template support

## License

MIT
