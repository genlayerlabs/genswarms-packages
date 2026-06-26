# GenSwarms Packages (`gsp`)

`gsp` is the open CLI for sharing and consuming **GenSwarms** packages — bodies,
policies, handlers and whole swarms — content-addressed and provable. You author
packages offline, publish them to a notary, and resolve or verify any of them by a
stable reference like `swarmidx:scope/name@0.1.0`.

The guarantee is **"trust the math, not the server."** Every published release is a
`name → digest` mapping signed into an append-only **transparency log**. `gsp log`
fetches that log and re-verifies it entirely on your machine — it recomputes the
SHA-256 hash chain and checks every Ed25519 signature. The notary never holds your
bytes; those stay in your git. It only records and signs the mapping, and you can
prove it never lied.

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

Using an AI agent? The repo ships a [Claude Code skill](.claude/skills/gsp/SKILL.md)
the agent can load to drive `gsp` directly.

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

## How it fits GenSwarms

The package IR (`swarm.state` / `swarm.overlay`) is the contract with
[genswarms](https://github.com/genlayerlabs/genswarms), the runtime that actuates
overlays on a live swarm. `gsp` authors and validates that IR offline and produces
overlays genswarms consumes; it never touches a running swarm itself.
`conformance/run.sh` folds the same inputs through both `gsp` (Go) and genswarms
(Elixir) and asserts they agree, so the two implementations can't silently drift.

See [`gsp-design-doc.md`](gsp-design-doc.md) for the full design.

## Build & test

```sh
cd cli
go build -o gsp .
go test ./...
```

(On Nix: `nix shell nixpkgs#go -c go build -o gsp .`)

## License

MIT — see [LICENSE](LICENSE). © 2026 GenLayer Labs Corp.
