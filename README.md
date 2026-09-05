# GenSwarms Packages (`gsp`)

`gsp` is the open CLI for sharing and consuming **GenSwarms** packages — bodies,
policies, handlers and whole swarms — content-addressed and provable. You author
packages offline, publish them to a notary, and resolve or verify any of them by a
stable reference like `swarmidx:scope/name@0.1.0`.

Every published release is a
`name → digest` mapping signed into an append-only **transparency log**. `gsp log`
fetches that log and re-verifies it entirely on your machine — it recomputes the
SHA-256 hash chain and checks every Ed25519 signature. The notary never holds your
bytes; those stay in your git. It only records and signs the mapping.

Use `gsp log --public-key HEX` with an independently obtained Ed25519 public key
to authenticate the returned chain. Without it, the command uses the key served
by the same endpoint: this checks internal consistency, not the server's identity.
Verification follows every returned page from genesis; `--since N` only filters
display, not verification. Malformed responses and non-progressing pages fail
closed, with a limit of 100,000 entries / 64 MiB of encoded log data.
Even with a pinned key, a signed prefix does not prove freshness or rule out
split views; that needs an independently trusted checkpoint or witness.
`resolve`, `materialize --resolve` and `vendor` **require** `--public-key HEX` or
`SWARMIDX_PUBLIC_KEY` (an independently trusted key; not a secret). They derive
release metadata directly from one verified log snapshot, including signed
withdrawals and exact-pin dependencies. They do not consult `/v1/resolve`.
Unsigned card fields such as `module` are excluded from resolved JSON; handler
entry points must come from `swarm-object.json` inside verified package bytes.
Materialization and vendoring of IR check existing digest pins and package slot
roles against the signed releases, rather than replacing mismatching pins.
Offline `materialize` without `--resolve` remains network/key-free.

Vendoring rejects traversal refs, symbolic-link package paths, symlink/special
file contents and linked destinations. File access is confined to opened
directory handles. New packages are copied into private staging, hashed there,
then renamed into place; existing packages are re-verified but **never repaired
or overwritten automatically**. If you edited a vendored copy, keep it and choose
a fresh vendor directory to fetch a clean copy. `vendor-lock.json` is written
through a temporary file and renamed, never truncated through a link.

`local:` sources are disabled by default even when signed. For a trusted local
fixture, grant access explicitly with a narrow directory:

```sh
gsp vendor --local-source-root /path/to/approved/sources --dir vendor/swarmidx swarmidx:scope/name@1
```

This permits local source reads only beneath that directory; it is independent
of the notary key. GitHub sources do not require this flag. These are per-package
staging guarantees, not a transaction over an entire dependency batch: earlier
successful packages can remain if a later package fails, and concurrent-writer
coordination and power-loss durability are not yet guaranteed.

## Install

Grab a prebuilt binary for your platform from
[Releases](https://github.com/genlayerlabs/genswarms-packages/releases/latest):

```sh
curl -L -o gsp https://github.com/genlayerlabs/genswarms-packages/releases/latest/download/gsp-linux-amd64
chmod +x gsp && sudo mv gsp /usr/local/bin/
gsp --help
```

Or build from source (Go 1.25+):

```sh
go install github.com/genlayerlabs/genswarms-packages/cli@latest   # installs as `cli`
mv "$(go env GOPATH)/bin/cli" "$(go env GOPATH)/bin/gsp"           # rename if you like
```

Using an AI agent? The repo ships [Claude Code skills](.claude/skills/) it can
load — [`gsp-use`](.claude/skills/gsp-use/SKILL.md) to drive `gsp` directly, and
[`gsp-contribute`](.claude/skills/gsp-contribute/SKILL.md) to develop it.

## Usage

`gsp` has two sides: an **offline authoring plane** (deterministic, no network,
never touches a live swarm) and a **notary client**.

```
# offline — deterministic, no network
gsp dirhash <dir>                                reproducible digest of a package dir (sha256:…)
gsp materialize [--resolve] <seed> [overlay…]    fold overlays onto a seed → materialized swarm.state
gsp verify <ir.json>                             validate a swarm.state or swarm.overlay
gsp manifest <swarmidx.json>                     validate a package manifest
gsp add <pkgref> --as agent:NAME|object:NAME     author an add_agent / add_object overlay
gsp bump <target> --field F --from D --to D       author a bump_package overlay

# notary client
gsp publish <swarmidx.json> --version V --source S   publish each package in the manifest
gsp resolve <ref>                                    resolve swarmidx:scope/name@version → digest
gsp log                                              fetch + verify the transparency log
```

### Publishing

Point `gsp` at a notary and give it a token (create one in the notary's web UI,
under Tokens). The hosted notary is `https://swarmidx.ygr.ai`:

```sh
export SWARMIDX_ENDPOINT=https://swarmidx.ygr.ai
export SWARMIDX_TOKEN=gsp_live_…
# Set SWARMIDX_PUBLIC_KEY to the notary's 64-hex public key, obtained
# independently from its operator. Do not bootstrap trust from /v1/publickey.

gsp publish swarmidx.json --version 0.1.0 --source github://owner/repo@main
gsp resolve swarmidx:you/web-researcher@0.1.0
gsp log
```

The notary is **zero-trust**: it clones your `--source` and re-hashes the package
dir itself, then signs the result — it never takes your word for the digest. Two
things follow:

1. the source repo must be reachable by the notary (a public `github://…`), and
2. `dir` paths in the manifest are relative to the source's **repo root** (run
   `gsp publish` from there so your local hashes match the notary's).

Versions are immutable — republishing the same `scope/name@version` is rejected;
bump the version instead.

### Local cross-repository verification

With a built `gsp` and Python dependencies from `swarmidx/backend/requirements.txt`:

```sh
python conformance/notary.py --swarmidx /path/to/swarmidx --gsp /absolute/path/to/gsp
```

This runs the actual CLI against Django over loopback: authenticated publishing,
Python signing, verified resolution, IR materialization and local vendoring.
It checks dependency hashes, signed withdrawals, altered index metadata,
wrong keys, tampered logs and mismatching IR pins/kinds. It uses public fixture
keys and a fresh in-memory test DB, never an existing DB or hosted notary.
It also checks explicit local-source authority and preservation of an edited
vendored package and its lock. It does not test PostgreSQL append concurrency,
remote git transport or BEAM loading/restart. Multi-package transactionality,
concurrent-writer coordination and crash durability remain separate work.

## How it fits GenSwarms

The package IR (`swarm.state` / `swarm.overlay`) is the contract with
[genswarms](https://github.com/genlayerlabs/genswarms), the runtime that actuates
overlays on a live swarm. `gsp` authors and validates that IR offline and produces
overlays genswarms consumes; it never touches a running swarm itself.
`conformance/run.sh` folds fixtures through both `gsp` (Go) and genswarms
(Elixir) and compares every parsed state field, including backend options,
images, TUI clients, model policies, object configuration and swarm options.
It checks semantic equality, not JSON byte identity; omitted defaults and
whitespace may differ. This is an offline data-contract check, not proof of
live execution or database restart behavior.

See [`gsp-design-doc.md`](gsp-design-doc.md) for the full design.

## What is a package (and what is not)

A package's `kind` is a **slot role** in the swarm IR — so the boundary is:

> A package is what a swarm references by content to constitute itself: something
> that fills an IR slot. Operational test: can `gsp add` or
> `gsp materialize --resolve` do anything with it? If not, it is not a package.

In: agent bodies, policies, object handlers, whole swarms. Out, each with its own
channel: **substrate** (the engine, runtimes, the LLM router — swarms run ON them,
no slot references them), **external clients/observers** (UIs, frontends, CLIs —
no slot, nothing to resolve; they are deploy artifacts, linked from a package's
card via the manifest's `docs`/`skill` fields), and **host code by definition**
(behaviour implementations a package deliberately leaves to the host app).

To turn a library/boot-script capability into a package, **objectify it first**:
wrap its lifecycle in an object handler (`init`/`terminate`/`handle_message` with
a minimal JSON protocol, config as pure data, module refs resolved without atom
minting, no compile dep on the engine), point the manifest's `dir` at exactly what
a swarm loads, then publish. Reference implementations: `genswarms-telegram`
(Ingress/Sender) and `genswarms-dashboard` (`Objects.Dashboard`). Full criterion
and the objectifying checklist: design doc §6.1.

## Build & test

```sh
cd cli
go build -o gsp .
go test ./...
```

(On Nix: `nix shell nixpkgs#go -c go build -o gsp .`)

## License

MIT — see [LICENSE](LICENSE). © 2026 GenLayer Labs Corp.
