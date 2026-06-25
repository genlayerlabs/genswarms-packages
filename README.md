# GenSwarms Packages (`gsp`)

The open client + spec for sharing [GenSwarms](https://github.com/genlayerlabs/genswarms)
packages — so the people already building swarms can **share** their bodies,
policies, handlers, agents, swarms and configurations, content-addressed and
provable.

This is the **public, MIT** half: the `gsp` CLI, the design doc, the examples and
the conformance harness. The hosted notary service ([swarmidx](https://github.com/genlayerlabs/swarmidx))
is separate — on purpose. `gsp` **verifies** the notary's transparency log
client-side (`gsp log` recomputes the SHA-256 chain and checks every Ed25519
signature), so the client must be open for "trust the math, not the server" to
mean anything.

Three planes, deliberately separate:

- **`gsp` CLI (this repo, `cli/`)** — the offline authoring plane (deterministic:
  dirhash, fold/materialize, validate, author overlays) **and** the notary client
  (publish / resolve / verify the log). A single static Go binary, agent-friendly.
  **Never touches a live swarm.**
- **`swarmidx`** — the notary: name→digest resolution + a signed transparency log,
  GitHub-delegated identity. Not a blob host (the bytes live in your git). Lives in
  its own repo; `gsp` talks to it over HTTP.
- **Control plane (live, OTP)** — already in
  [genswarms](https://github.com/genlayerlabs/genswarms): it actuates overlays on
  the running supervision tree. `gsp` produces overlays; genswarms consumes them.
  The overlay IR (`swarm.overlay`) is the contract between the two.

See [`gsp-design-doc.md`](gsp-design-doc.md) for the full design.

## The CLI

The IR contract (`swarm.state` / `swarm.overlay`) is owned by genswarms (Elixir);
this Go CLI conforms to that same JSON contract — `conformance/run.sh` folds the
same inputs through both and asserts they agree, so a divergence is a failing test,
not a silent drift.

```
# offline (deterministic, no network)
gsp dirhash <dir>                     reproducible digest of a package dir (sha256:…)
gsp materialize [--resolve] <seed> [overlay…]   fold overlays → materialized swarm.state
gsp verify <ir.json>                  parse + validate a swarm.state or swarm.overlay
gsp manifest <swarmidx.json>          parse + validate a package manifest
gsp add <pkgref> --as agent:NAME|object:NAME    author an add_agent/add_object overlay
gsp bump <target> --field F --from D --to D     author a bump_package overlay

# notary client (--endpoint / $SWARMIDX_ENDPOINT, --token / $SWARMIDX_TOKEN)
gsp publish <swarmidx.json> --version V --source S   dirhash each dir and publish it
gsp resolve <ref>                     resolve swarmidx:scope/name@version → digest
gsp log                               fetch + verify the transparency log (Ed25519)
```

Build & test (NixOS):

```bash
cd cli
nix shell nixpkgs#go -c go build -o gsp .
nix shell nixpkgs#go -c go test ./...
```

## License

MIT — see [LICENSE](LICENSE).
