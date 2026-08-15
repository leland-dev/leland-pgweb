# pgweb

Simple web-based and cross platform PostgreSQL database explorer.

[![Release](https://img.shields.io/github/release/sosedoff/pgweb.svg?label=Release)](https://github.com/sosedoff/pgweb/releases)
[![Linux Build](https://github.com/sosedoff/pgweb/actions/workflows/checks.yml/badge.svg)](https://github.com/sosedoff/pgweb/actions?query=branch%3Amain)
[![Go Report Card](https://goreportcard.com/badge/github.com/sosedoff/pgweb)](https://goreportcard.com/report/github.com/sosedoff/pgweb)
[![GoDoc](https://pkg.go.dev/badge/github.com/sosedoff/pgweb)](https://pkg.go.dev/github.com/sosedoff/pgweb)
[![Docker Pulls](https://img.shields.io/docker/pulls/sosedoff/pgweb.svg)](https://hub.docker.com/r/sosedoff/pgweb/)

## Overview

Pgweb is a web-based database explorer for PostgreSQL, written in Go, and works
on Mac, Linux and Windows machines. Distributed as a simple binary with zero dependencies.
Very easy to use and packs just the right amount of features.

[See application screenshots](SCREENS.md)

## Features

- Cross-platform: Mac/Linux/Windows (64bit).
- Simple installation (distributed as a single binary).
- Zero dependencies.
- Works with PostgreSQL 9.6+.
- Supports native SSH tunnels.
- Multiple database sessions.
- Execute and analyze custom SQL queries.
- Table and query data export to CSV/JSON/XML.
- Query history.
- Server bookmarks.

Visit [WIKI](https://github.com/sosedoff/pgweb/wiki) for more details.

## Demo

Visit https://pgweb-demo.fly.dev/ to see Pgweb in action.

## Installation

- [Precompiled binaries](https://github.com/sosedoff/pgweb/releases) for supported operating systems are available.
- [More installation options](https://github.com/sosedoff/pgweb/wiki/Installation)

## Usage

Start server:

```
pgweb
```

You can also provide connection flags:

```
pgweb --host localhost --user myuser --db mydb
```

Connection URL scheme is also supported:

```
pgweb --url postgres://user:password@host:port/database?sslmode=[mode]
pgweb --url "postgres:///database?host=/absolute/path/to/unix/socket/dir"
```

### Multiple database sessions

To enable multiple database sessions in pgweb, start the server with:

```
pgweb --sessions
```

Or set environment variable:

```
PGWEB_SESSIONS=1 pgweb
```

## Testing

Before running tests, make sure you have PostgreSQL server running on `localhost:5432`
interface. Also, you must have `postgres` user that could create new databases
in your local environment. Pgweb server should not be running at the same time.

Execute test suite:

```
make test
```

If you're using Docker locally, you might also run pgweb test suite against
all supported PostgreSQL version with a single command:

```
make test-all
```

## Deployment

This fork is deployed to AWS ECS as an internal, read-only database browser. The
infrastructure lives in `leland-monorepo` under
`terraform/services/leland-shared/modules/pgweb/`; this repo only supplies the
image and the script that ships it.

```
script/deploy.sh --profile staging-hosting
script/deploy.sh --profile prod-hosting --environment prod
```

That does four things:

1. Builds the image for `linux/amd64` and tags it with the short git SHA
2. Pushes it to the environment's ECR repository
3. Registers a new task definition revision with the `pgweb` container repointed
   at that image
4. Points the ECS service at the new revision and waits for it to stabilize

Requires `docker`, `aws`, `jq`, and a valid SSO session
(`aws sso login --profile <profile>`). Run `script/deploy.sh --help` for the
remaining flags (`--no-wait`, `--yes`, `--region`).

### Why a script instead of Terraform

Terraform **bootstraps** the task definition and ECS service, then stops managing
the parts that change at deploy time — it declares `ignore_changes` on
`container_definitions` and `task_definition`. Revisions are expected to be
registered out of band, and a `terraform apply` will not revert them. So the
deploy path is "register a new revision, point the service at it", which is
exactly what the script does.

To change the Terraform baseline itself (the seed used when standing up a new
environment), edit the module and
`terraform apply -replace='module.pgweb.aws_ecs_task_definition.pgweb'`.

### Things worth knowing

**The task runs two containers.** Alongside `pgweb` is a `cloudflared` sidecar
that opens an outbound tunnel to Cloudflare's edge. There is no ALB, no public
DNS record, and no inbound security-group rule — the tunnel is the only way in,
with Cloudflare Access in front of it for authentication. The script reads the
live task definition and swaps *only* the `pgweb` container's image, leaving the
sidecar, secrets, and log configuration untouched.

**Images must be amd64.** The task definition declares
`runtime_platform.cpu_architecture = X86_64`. The script always builds with
`--platform linux/amd64`, so a build on an Apple Silicon Mac still produces a
runnable image — without that, the task fails to start with an exec-format error
that reads like a corrupted image.

**ECR tags are immutable.** A tag can only be pushed once, which has two
consequences. Deploying with uncommitted changes is refused outright: the tag
would name a commit whose content it does not contain, and that could never be
corrected by a later push. And redeploying a commit already in ECR reuses the
existing image rather than rebuilding it — under immutability, that image *is*
that commit's build.

**The environment picks the account.** Staging and prod are separate AWS
accounts, each with its own `leland-pgweb` repository. Since `--profile` and
`--environment` are independent flags, the script verifies the profile resolves
to the account the environment expects and refuses on a mismatch.

### Rolling back

The script only moves forward. To go back, point the service at an earlier
revision directly:

```
# List recent revisions
aws ecs list-task-definitions --profile <profile> --region us-west-2 \
  --family-prefix leland-pgweb-<env> --sort DESC --max-items 10

# Roll back to a specific one
aws ecs update-service --profile <profile> --region us-west-2 \
  --cluster leland-<env>-frontend-cluster \
  --service leland-pgweb-<env> \
  --task-definition leland-pgweb-<env>:<revision>
```

Container logs are in the CloudWatch log group `/ecs/leland-pgweb-<env>`, under
the `pgweb/*` and `cloudflared/*` stream prefixes.

## Contribute

- Fork this repository
- Create a new feature branch for a new functionality or bugfix
- Commit your changes
- Execute test suite
- Push your code and open a new pull request
- Use [issues](https://github.com/sosedoff/pgweb/issues) for any questions
- Check [wiki](https://github.com/sosedoff/pgweb/wiki) for extra documentation

## License

The MIT License (MIT). See [LICENSE](LICENSE) file for more details.
