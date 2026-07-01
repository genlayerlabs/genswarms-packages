---
name: gsp-contribute
description: >-
  Develop the gsp CLI — the single static Go binary for content-addressed
  GenSwarms packages. Load this before changing anything under cli/: the IR
  package (a second implementation of the genswarms IR that must stay in
  conformance), the dirhash digest (byte-compatible with the swarmidx server),
  the notary client, or adding a command. To merely use gsp, use gsp-use.
---

# Contributing to gsp

`gsp` is one static Go binary (`cli/`, Go 1.25) with an **offline authoring
plane** and a **notary client**. Its correctness rests on two
cross-implementation contracts — break either silently and the whole
"trust the math" promise fails. Read `gsp-design-doc.md` before changing the IR
or the digest.

## Layout

```
cli/main.go              command dispatch (dirhash, materialize, verify, manifest,
                         add, bump, publish, resolve, log) + each cmdX(args) handler.
cli/internal/ir/         the GenSwarms IR in Go: state.go, overlay.go, fold.go,
                         encode.go, resolve.go, ref.go, helpers.go. THE port.
cli/internal/dirhash/    the reproducible package digest (sha256:…).
cli/internal/manifest/   swarmidx.json package manifest validation.
cli/internal/client/     the swarmidx notary client (publish/resolve/log).
conformance/             cross-impl harness (run.sh + genswarms_fold.exs).
examples/                seeds, overlays, packages, a sample swarmidx.json.
```

## Contract 1 — the IR must stay in conformance with genswarms

`cli/internal/ir` is a **second implementation** of the genswarms IR (the first
is the Elixir one in `genlayerlabs/genswarms`). Given the same seed + overlays,
`gsp` fold **must** produce byte-identical `swarm.state` to the Elixir fold. The
harness proves it:

```sh
GENSWARMS=/path/to/genswarms GSP=/path/to/gsp ./conformance/run.sh   # needs mix + jq
```

If you change `fold.go`, `overlay.go`, `state.go`, `encode.go`, or the op set,
you are changing a shared semantics: either the change already matches the Elixir
side, or **both implementations must move together** and the conformance run must
stay green. A gsp-only change to fold semantics is a divergence bug, not a
feature. Unknown overlay ops must fail — no silent ignore, no inventing ops.

## Contract 2 — dirhash is byte-compatible with the swarmidx server

`gsp dirhash <dir>` and the **swarmidx** notary's server-side dirhash must compute
the **same** `sha256:…` for the same tree — the notary is zero-trust: it clones
`--source` and re-hashes the dir itself, then rejects a `digest mismatch`. So
`cli/internal/dirhash` is a wire contract with the server (`genlayerlabs/swarmidx`),
not a private helper. Any change (traversal order, normalization, what's
included/excluded) must land on **both** sides or every publish breaks.

## Adding a command

1. Add a `case "<name>":` in `main.go`'s dispatch and a `cmd<Name>(args []string)`
   handler with its own `flag.NewFlagSet`.
2. Keep offline commands offline — only `publish`, `resolve`, `log`, and
   `materialize --resolve` (which resolves `swarmidx:` refs via `internal/client`)
   may touch the network; every other command (and `materialize` without
   `--resolve`) must be deterministic and network-free.
3. Reuse `internal/ir` / `internal/manifest` for validation rather than
   re-parsing; validation lives there.

## Transparency-log verification stays client-side

`gsp log` recomputes the SHA-256 hash chain and checks every Ed25519 signature
locally — that client-side verification is *why* the client half is open source.
Never move a verification step server-side or trust a server-asserted result; the
guarantee is "trust the math, not the server."

## Tests & CI

```sh
cd cli && go test ./...        # dirhash_test, ir_test, manifest_test
go build -o gsp ./             # or: go install ./...
```

CI: `.github/workflows/cli.yml` (build + test), `.github/workflows/release.yml`
(prebuilt binaries). Add a table-driven test next to the package you touch; for a
change to fold/overlay semantics, add a fixture under `examples/` and extend the
conformance harness so the parity is checked, not assumed.

## Where to read more

- `gsp-design-doc.md` — the package model, the IR, the transparency-log design.
- `README.md` — the CLI surface + the publish model.
- `genlayerlabs/swarmidx` — the notary server (the other side of the dirhash /
  transparency-log contracts). The `swarmidx-*` skills cover it.
- `genlayerlabs/genswarms` (`intermediate-representation.md`) — the reference IR
  the `gsp` IR must match.
