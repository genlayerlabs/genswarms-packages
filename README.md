# GenSwarms Packages (`gsp`)

The packaging, distribution and composition layer for [GenSwarms](https://github.com/genlayerlabs/genswarms)
swarms — so the people already building on GenSwarms can **share** their
components, objects, skills, agents, swarms and configurations.

Three planes, deliberately separate:

- **`gsp` CLI (this repo, `cli/`)** — the *offline authoring plane*. Deterministic:
  resolves refs, computes reproducible digests, folds overlays and validates IR.
  A single static Go binary, agent-friendly. **Never touches a live swarm.**
- **`swarmidx` (planned)** — the *index / notary*: a name→digest resolver plus a
  signed transparency log, GitHub-delegated identity. Not a blob host — the bytes
  live in your git.
- **Control plane (live, OTP)** — already exists in
  [genswarms](https://github.com/genlayerlabs/genswarms): it actuates overlays on
  the running supervision tree. `gsp` produces overlays; genswarms consumes them.
  The overlay IR (`swarm.overlay`) is the contract between the two.

See [`gsp-design-doc.md`](gsp-design-doc.md) for the full design.

## The CLI (offline plane)

The IR contract (`swarm.state` / `swarm.overlay`) is owned by genswarms (Elixir);
the Go CLI conforms to that same JSON contract — a divergence shows up as a failing
conformance test, not a silent drift.

```
gsp dirhash <dir>                   reproducible digest of a package dir (sha256:…)
gsp materialize <seed> [overlay…]   fold overlays onto a seed → materialized swarm.state
gsp verify <ir.json>                parse + validate a swarm.state or swarm.overlay
gsp manifest <swarmidx.json>        parse + validate a package manifest
```

Build & test (NixOS):

```bash
cd cli
nix shell nixpkgs#go -c go build -o gsp .
nix shell nixpkgs#go -c go test ./...
```

## Status

First slice: the offline `gsp` core (dirhash, IR parse/validate, fold/materialize,
manifest) — no service, no accounts, no network. `swarmidx` (the notary + identity)
and `publish`/`resolve`-from-registry come next.
