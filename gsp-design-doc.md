# GenSwarms Packages (`gsp`) — Design document

**Status:** design draft · v0.2
**Scope:** package index + CLI for GenSwarms swarm intermediate representations (IR), and the live-mutation model over the runtime.

> **Change v0.1 → v0.2:** swarms mutate hot while they are live. That settles open question #1 (overlay-only vs in-place → **the overlay IS the control plane**) and forces modeling the lifecycle (three layers, desired/observed) and per-op transition semantics. New sections: §3 and §4.

---

## 1. What `gsp` is (and what it is not)

`gsp` is the packaging, distribution and composition system for GenSwarms swarms. Two planes must be distinguished from the start because they have different responsibilities and different clocks:

- **gsp / authoring time (offline, deterministic):** resolves refs → digests, vendors bytes, emits *resolved* overlays. Applies nothing to a live system.
- **runtime / control plane (live):** the BEAM controlling the swarms consumes overlays and **actuates** them on the OTP supervision tree, assigns `seq`, handles drain/atomicity/recovery.

The **overlay IR (IR2) is the contract between the two planes.** `gsp` produces resolved overlays; the runtime actuates them.

Pieces:

1. **The index (`swarmidx`)** — a service mapping human-readable references to content digests and notarizing them. **Not a blob host.**
2. **The CLI (`gsp`)** — the primary authoring interface, designed to be driven by a human *or an agent* (Claude Code).
3. **The control plane** — the Elixir/OTP runtime applying overlays to live swarms (§4).

The index website exists only to discover/browse packages (analogous to crates.io's site next to `cargo`).

### The index's guiding principle: notary, not warehouse

npm/crates.io/PyPI host the bytes because they predate ubiquitous git hosting. Go proved it is unnecessary: `proxy.golang.org` + `sum.golang.org` are a **resolver** (`(name, version) → digest`) and a **transparency log** signing that mapping; the bytes live on GitHub, content-addressed.

`swarmidx` follows that model. In essence it is one function:

```
swarmidx:jmlago/web-researcher@1.2.3  →  sha256:9f2c…  (source: github://jmlago/agents@v1.2.3, dir: packages/web-researcher)
```

plus the transparency log. The whole registry fits in a name→digest map + a Merkle log. No petabytes to operate. It minimizes operational surface and maximizes sovereignty/reproducibility.

---

## 2. The two intermediate representations

### IR1 — `swarm.state`

Snapshot of a swarm: `agents`, `objects`, `topology`, `options`. Each agent has typed slots (`body`, `model`, `backend`); each object has a `handler`. Refs carry `ref`, `digest` (where applicable) and `kind`.

**`swarm.state` is ambiguous without a phase discriminator** (see §3): the same IR1 can be *desired* (what is wanted) or *observed* (what the runtime IS right now). It carries `phase: desired | observed`.

### IR2 — `swarm.overlay`

An ordered event log over a `swarm.state`: `add_agent`, `add_topology_edges`, `scale_agent_group`, `bump_package`, `remove_agent`, … Two simultaneous roles:

- **Version control:** semantic, replayable history with blame and time-travel.
- **Control plane:** each event is a *command to a live system* (§4).

**Decision:** CLI and runtime mutations are expressed as IR2 events. Nobody edits IR1 in place.

---

## 3. Three-layer model and lifecycle

Settles the question "overlay-only, or IR1 carrying gsp info?": **it is not a XOR.** There are three layers.

1. **seed IR1** — authored, immutable, the origin. May carry loose refs (`@^1.2`, no digest).
2. **overlay log IR2** — append-only, the truth of *change* and the control plane.
3. **materialized IR1** = `fold(seed, overlays)` + digest resolution. It is both the **desired** state and the **checkpoint** (recovery + compaction).

So "IR1 carries gsp info" is true — but for the **materialized** IR1 (an output), not the **seed** (the source). The seed stays clean; mutations live in overlays; digests land on the materialized artifact.

### Desired vs Observed

The moment there is a live system there are two states that diverge:

- **Desired** = `fold(seed, overlays)` + resolve. What is wanted.
- **Observed** = what the OTP tree *is* right now (live agents, mailboxes, in-flight messages). What the panel renders ("agents 4 · supervised · t+00:04").

Divergence example: `scale coder → 3` is desired; if a `coder` crashes and restarts, observed is 2 for an instant. **The reconciler lives in the gap** and drives observed → desired. This *is* "supervised recovery": after a crash, the system reconciles against the desired state derived from log + checkpoint.

### Authored vs resolved (still holds)

The authored seed carries constraints (`@^1.2`); `gsp resolve` compiles it to resolved form with inline digests. Hylomorphism: the authored form is form/potency, the resolved form is the pinned act.

---

## 4. Live mutation (control plane)

Swarms mutate **while live**. Direct consequence: in-place edits over IR1 are ruled out — you do not "edit the config file" of a running OTP supervision tree; you send it commands. The overlay **is** the control plane (the Kubernetes model: desired state + reconciler; in this stack: dynamic child-spec changes + hot code loading over supervisors).

### 4.1 Ordering: single-writer by construction

The overlay log is always written by **the same BEAM that controls the swarm**. Therefore:

- `seq` is assigned by that single process → **the required total order, with no consensus protocol.** It is a single-writer log by architecture, not by convention.
- No `seq` race is possible: the emitter (agent, operator, autoscaler) *proposes* an intent; the controlling BEAM is what *sequences* it and materializes it into the log.

> **Future (outside v1):** if HA with replicated controllers is ever wanted, *then* the log would need replication/consensus. Not today.

### 4.2 Per-op transition semantics

What hot mutation adds and the offline model ignored. Every op touching a live system carries its policy in the payload:

- **`remove_agent` / drain:** what happens to in-flight work? Policy `on_inflight: drain | kill | quarantine`.
  - `drain`: let current work finish, accept nothing new, then terminate.
  - `kill`: discard in-flight immediately.
  - `quarantine`: stop accepting, freeze, defer the decision.
- **`bump_package` / hot reload (hard requirement):** must be able to bump mid-flight, live. It is hot code reload on a live agent. Policy `migration: state_migrate | restart`.
  - `state_migrate`: preserves accumulated state across the swap (`code_change/3` style).
  - `restart`: discards state, starts clean on the new version.
  - Combines with `on_inflight` for the old version's in-progress work.
- **Multi-event overlay atomicity:** an overlay with N events on a live system: `apply: incremental | transactional`.
  - `incremental`: applied one by one; every op must leave the supervision tree **valid in between**; intermediate states are observable.
  - `transactional`: stage → commit; the swarm is never observable in a partial state.

### 4.3 Rollback → roll-forward

Offline, "rollback to seq N" = recompute the fold. **Live is not reversible:** you cannot un-spawn a process or un-send a message; a removed agent's state is gone. Live systems do **roll-forward with compensating events**, not literal reversion.

- Time-travel for **inspection**: free (fold up to seq N).
- Time-travel for **actuation**: emit compensating events.

### 4.4 Checkpoint = materialized IR1

The materialized IR1 is not just a build artifact — it is a **checkpoint** with operational meaning:

- **Recovery:** restore a swarm from the last checkpoint instead of replaying the whole log.
- **Compaction:** snapshot the state + truncate the log up to that `seq`. Solves unbounded log growth.

It is event sourcing's snapshot pattern applied to the runtime.

### 4.5 The supply-chain ↔ runtime bridge

`bump_package` is where the two worlds touch:

1. `gsp` resolves the new digest (offline, against `swarmidx` + the transparency log) and emits the resolved `bump_package` event.
2. The runtime applies the hot-swap under the `migration` policy.

The **transparency log covers the "which bytes"**; the **runtime covers the "how state migrates"**. Together they give an end-to-end supply-chain story over the swarm's live evolution.

---

## 5. Reference taxonomy

The "service ref vs package ref" axis is wrong (OCI is service-external *yet* hashable). There are **two orthogonal axes**:

| ref | who resolves | content-addressable |
|---|---|---|
| `swarmidx:jmlago/coder@0.4.0` | swarmidx | yes (digest) |
| `oci:szc-agent-code` | external (OCI) | yes (digest) |
| `openrouter:anthropic/claude-sonnet-4` | external | no (`attested`) |
| `ssh` / `host` | external | no |

`swarmidx` is only responsible for the `swarmidx:` row. The rest it **passes through**: for OCI it delegates digest verification to the OCI registry; for non-hashable refs it records at most the `attested` flag.

---

## 6. Package kinds

In the manifest (`swarmidx.json`), `kind` is the **slot role**, not the byte type:

- `body` — agent persona/definition (data)
- `policy` — an `llm-policy` IR (data)
- `handler` — object code (code)
- `swarm` — a complete, resolved swarm IR published as a composable unit

The resolver uses `kind` for **slot typing**: it rejects dropping a `kind: handler` into a `model` slot, etc.

> **Open:** `data`/`code` looks *derivable* from the kind (body/policy → data, handler → code). Confirm no kind is sometimes one and sometimes the other; if none is, it is not a manifest field.

### 6.1 What should be a package (and what should not)

The criterion follows from "kind = slot role" and is worth making explicit:

> **A package is what a swarm references by content to constitute itself: what
> fills an IR slot.** The operational test: *can `gsp add` or
> `gsp materialize --resolve` do anything with it?* If the answer is no, it is
> not a package — it is something else with its own channel.

Passing the criterion: bodies (what the agent is), policies (how it picks a
model), handlers (what object runs), swarms (the whole composition). **Three
classes** stay out, each with its own channel:

1. **Substrate** — the genswarms engine, subzeroclaw, the LLM router. The swarm
   runs *on* them; no slot references them. Their channel: their own releases/
   installation. Packaging them would be cataloguing infrastructure, not
   composing the swarm.
2. **External clients and observers** — UIs, frontends, CLIs, anything watching
   the swarm from outside through an API. They have no slot, the topology does
   not gate them, the resolver has nothing to resolve. Their channel: deploy
   artifacts (image, repo), linked from a package's card when they accompany
   one (the `docs`/`skill` fields, §7).
3. **Host code by definition** — implementations of behaviours a package
   deliberately leaves to the host (a `DataSource`, an app-specific adapter).
   App-specific by design: there are no general bytes to notarize. Their
   channel: the host's repo. (The *reference* adapter between two packages
   lives in the package that owns the specifics — e.g. the reference
   DataSource for a Telegram transport belongs to the Telegram package, not
   to the host.)

**Objectifying: turning a capability into a package.** The path for something
that is a library/boot-script today and *should* be slot content is to give it
object form first:

1. Wrap the lifecycle in an **object handler** (`init/1` boots from pure-data
   config, `terminate/2` stops deterministically, `handle_message/3` speaks a
   minimal JSON protocol — at least `{"action":"status"}` —, `interface/0`
   documents it).
2. Config = **data, not code**: module refs as atoms (Elixir defs) or strings
   (JSON IR) resolved with `to_existing_atom` (no atom minting; unknown module
   ⇒ init fails, fail-closed). Usable defaults with zero host code where
   possible (a `Null` data source beats a required one).
3. No compile-time dep on the engine: callbacks **by convention**, not
   `@behaviour` — genswarms is a peer/runtime dep and the library compiles on
   its own.
4. `swarmidx.json` with `kind: handler` and **`dir` pointing at exactly what
   the swarm loads** (if the repo also ships a frontend/client, keep it outside
   the `dir`: the digest must cover exactly the consumable).
5. Publish (tag). References: `genswarms-telegram` (Ingress/Sender) and
   `genswarms-dashboard` (`Objects.Dashboard` + `DataSource.Null`).

Adding a new `kind` for the excluded classes (e.g. `app`/`ui` for frontends)
was deliberately rejected: a kind without resolver semantics turns the registry
into a link catalog — a different finality. If a real story of per-swarm
operational attachments ever exists, it gets designed then, with its own
semantics.

---

## 7. The manifest: `swarmidx.json`

JSON across the whole system — one format, one parser, one mental model (the IRs are already JSON; so is the manifest). Where comments would be needed, a `description`/`note` field is used. At the repo root:

```json
{
  "registry": { "scope": "jmlago" },
  "packages": [
    { "name": "web-researcher", "dir": "packages/web-researcher", "kind": "body",
      "description": "Web research agent body" },
    { "name": "cost-router", "dir": "packages/cost-router", "kind": "policy",
      "description": "Cost-aware LLM routing policy" },
    { "name": "task-board", "dir": "packages/task-board", "kind": "handler" },

    { "name": "strict-reviewer", "dir": "packages/strict-reviewer", "kind": "body",
      "note": "explicit deps ONLY when not inferable from content (see §9)",
      "deps": ["jmlago/cost-router@^1.0"] }
  ]
}
```

- `scope` **must** match the GitHub owner; verified at publish.
- **No per-package `version` field** — the tag provides the version (§8).
- `swarmidx.json` itself is not hashed: what gets dirhashed is each `dir`'s content, not the manifest.
- Card metadata (optional, not notarized): `description`, plus `docs` / `skill` —
  a path relative to the source root (e.g. `docs/guide.md`) or an absolute URL.
  When the manifest does not declare them, the notary detects `README.md` /
  `SKILL.md` in the package `dir` (in the same clone it hashes) and uses them
  as fallback.

---

## 8. Versioning and publishing

### The tag is the version

`version` in the manifest *and* a tag = two sources of truth that diverge. **The manifest describes structure; the tag provides the version.** One source for each thing (the Go modules lesson).

Publishing:

```
git tag v1.2.3 && git push --tags
```

The registry reads `swarmidx.json` at that commit, computes each `dir`'s digest, and records `(jmlago/<name>, 1.2.3) → digest, source`. Via webhook (deno.land/x style) no separate `gsp publish` step is needed, though it can be offered as a shortcut. GitHub identity gives the namespace for free (`jmlago/` bound to the handle) and kills squatting.

### Monorepo: lockstep now, per-package later

- **Lockstep (recommended to start):** one repo = one version line; one tag versions every package in the manifest.
- **Per-package (future):** Go-style per-subdirectory tags (`coder/v0.4.0`). Migrate only if a package's cadence truly diverges.

---

## 9. Dependencies

### Rule: declare only what is not inferable from content

- **`kind: swarm`** → **needs no `deps` section.** Its deps *are* the refs already living in its IR. The resolver walks them.
- **`kind: body` / `policy` / `handler`** with deps not discoverable from the slot (a body with a pinned policy, a handler importing another handler) → declared in `deps`. Object→object (`task-board` depends on `kv-store`) is this case.

### Two distinct graphs — do not conflate

- **Dependency graph** (who-I-need-to-resolve): **mandatory DAG.** A `kind: swarm` referencing another `kind: swarm` is an edge of this graph → cycles forbidden.
- **Message topology** (who-talks-to-whom): **may have cycles** (`researcher ↔ task_board`). Independent of the deps graph.

### Merkle composition

Publishing a `kind: swarm` = publishing its resolved IR. Its transparency-log entry **commits transitively to the digests of all its deps**. If any dep changes, the swarm's digest changes.

---

## 10. Reproducible digest (`dirhash`)

The digest over a `dir` must be recomputable by any client. Do **not** hash the git tree or a tar (sensitive to order/mtime/permissions). Go-style dirhash: a sorted list of `sha256(bytes)  path` per file inside `dir`, then hash that list. Deterministic, independent of git packaging.

---

## 11. The CLI (`gsp`) — authoring, agent-facing

`gsp` operates on the offline plane: it resolves and emits *resolved* overlays; it never acts on live systems (that is the control plane, §4).

| Command | What it does |
|---|---|
| `gsp resolve` / `gsp sync <ir>` | Deterministic. Resolves refs, fills digests, vendors bytes. `cargo build` / `go mod download`. |
| `gsp add <pkg> --as <slot:name> [--wire <obj>] [--on-inflight drain] [--migration restart]` | Emits a `swarm.overlay` event (`add_agent`, …) already carrying its transition policy. |
| `gsp bump <agent> --field body [--migration state_migrate]` | Emits a resolved `bump_package` (new digest). The runtime does the hot-swap. |
| `gsp materialize <seed> <overlays...>` | `fold` + resolve → materialized IR1 (checkpoint / desired). |
| `gsp verify <ir>` | Recomputes digests and verifies against the transparency log. |
| `gsp publish` | Optional shortcut over the `git tag` flow. |

**Agent-facing requirements (Claude Code):** stable `--json` output, idempotency, semantic exit codes. Materializes the landing's promise ("hand it to your agent").

---

## 12. Transparency log

Given the `"attested": true` flag and the attestation/sovereignty orientation, the transparency log (append-only Merkle + signature, `sumdb` style) **is not optional**: it prevents a digest from being silently changed. It composes with `attested` and with `bump_package` (§4.5) into an end-to-end verification story for the IR, including its live evolution.

---

## 13. Open questions

1. ~~`gsp add`: overlay-only vs in-place~~ → **Settled (§3, §4):** the overlay is the control plane; three layers; in-place ruled out.
2. **`data`/`code`: derivable from `kind`, or an explicit field?** (§6)
3. **Transition-policy defaults** (`on_inflight`, `migration`, `apply`): which are safe by default? Probably `drain` + `restart` + `incremental`.
4. **Checkpoint/compaction cadence:** every N events, time-based, or explicit? (§4.4)
5. **Is a `deps` section needed on day one**, or deferred? (§9)
6. **Transparency log: v1 or a later iteration?**
7. **Local cache/vendoring:** where resolved bytes materialize and how they get garbage-collected.

---

## 14. Future direction (outside v1 scope)

- **Control-plane HA:** replicated controllers → the overlay log would need replication/consensus (§4.1). Today: single-writer per BEAM.
- **Phylogenesis:** `seed` (IR1) + overlays (IR2) ≅ genome + mutations; overlays as genetic diffs over the seed, with `gsp` as the distribution/versioning layer for genetic material.
