---
name: gsp
description: >-
  Author, publish, resolve and verify GenSwarms packages with the gsp CLI
  against a swarmidx notary. Use when sharing or consuming GenSwarms components
  (bodies, policies, handlers, agents, swarms), publishing a package to a
  notary, resolving a swarmidx: ref to a content digest, or verifying a
  transparency log. Also covers the offline authoring plane (dirhash,
  materialize/fold overlays, validate IR) that never touches a live swarm.
---

# gsp — the GenSwarms Packages CLI

`gsp` is a single static Go binary with two planes:

- **Offline authoring** (deterministic, no network): reproducible digests,
  fold/materialize overlays onto a seed, validate `swarm.state` / `swarm.overlay`
  IR and package manifests. **Never touches a live swarm.**
- **Notary client**: publish / resolve / verify against a **swarmidx** notary —
  a name→digest resolver plus a signed, append-only transparency log. The notary
  is **not** a blob host: the bytes live in your git; the notary records and
  signs `name → digest` mappings.

## Install

Prebuilt binary from [Releases](https://github.com/genlayerlabs/genswarms-packages/releases/latest),
or from source: `go install github.com/genlayerlabs/genswarms-packages/cli@latest`
(installs as `cli`; rename to `gsp`). Verify with `gsp --help`.

## Talking to a notary

Set these once (the public hosted notary is `https://swarmidx.ygr.ai`):

```sh
export SWARMIDX_ENDPOINT=https://swarmidx.ygr.ai   # or --endpoint
export SWARMIDX_TOKEN=gsp_live_…                    # or --token; create one in the notary UI under /tokens/
```

Your **scope** is your identity (your GitHub handle when you log in with GitHub).
Published refs look like `swarmidx:<scope>/<name>@<version>`.

## Core workflows

**Publish** a package set. The manifest (`swarmidx.json`) lists each package's
`name`, `kind` (`body` | `policy` | `handler` | `swarm`) and `dir`:

```sh
gsp publish swarmidx.json --version 0.1.0 --source github://OWNER/REPO@REF
```

**Resolve** a ref to its digest and provenance:

```sh
gsp resolve swarmidx:jmlago/web-researcher@0.1.0
```

**Verify** the whole transparency log client-side (recomputes the SHA-256 hash
chain and checks every Ed25519 signature — trust the math, not the server):

```sh
gsp log            # prints entries and "log verified: N entries, hash chain + Ed25519 signatures OK"
```

**Offline authoring** (no network, no token):

```sh
gsp dirhash <dir>                                 # reproducible sha256:… of a package dir
gsp materialize <seed> [overlay…]                 # fold overlays → materialized swarm.state
gsp verify <ir.json>                              # validate a swarm.state / swarm.overlay
gsp manifest <swarmidx.json>                      # validate a package manifest
gsp add <pkgref> --as object:NAME                 # author an add_object overlay (stdout or -o)
gsp bump <target> --field F --from D --to D       # author a bump_package overlay
```

## The publish model (read this before publishing — it has two non-obvious rules)

The notary is **zero-trust**: it does **not** accept your claimed digest. It
**clones the `--source` itself and re-hashes the package dir**, then signs the
`name → digest` into the log. Two consequences:

1. **The source must be reachable by the notary.** `github://…` is a real clone:
   the repo must be **public** (or the notary needs clone credentials). A private
   source fails the clone.
2. **`dir` is relative to the source's repo root, and must also exist locally.**
   The CLI hashes the dir locally to compare; the notary hashes the cloned dir.
   They must match. So the manifest's `dir` paths and your local checkout must be
   the repo root layout — e.g. if a package lives at `examples/packages/foo` in
   the repo, the manifest `dir` is `examples/packages/foo` (not `packages/foo`),
   and you run `gsp publish` from the repo root.

If the digests disagree you get a clean `digest mismatch`; if the dir isn't found
in the clone you get `dir … not found`.

## Conventions
- Versions are immutable: republishing the same `<scope>/<name>@<version>` is
  rejected. Bump the version.
- `kind: swarm` packages need no `deps` — their dependencies *are* the refs in
  their IR, walked by the resolver.
- The offline subcommands are safe to run anywhere; only publish/resolve/log
  reach the network.
