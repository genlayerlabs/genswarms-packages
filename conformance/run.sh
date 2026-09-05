#!/usr/bin/env sh
# Cross-impl conformance: gsp (Go) fold == genswarms (Elixir) fold, on the same
# seed + overlays. The gsp IR is a second implementation of the genswarms IR;
# compare complete parsed states for the checked fixtures.
#
# Requires: a genswarms checkout with mix and a built gsp.
#   GENSWARMS=/path/to/genswarms GSP=/path/to/gsp ./conformance/run.sh
set -eu

HERE="$(cd "$(dirname "$0")" && pwd)"
GENSWARMS="${GENSWARMS:-$HOME/docs/personal/genswarms}"
GSP="${GSP:-gsp}"
SEED="$HERE/../examples/research/seed.json"
WORK="$(mktemp -d)"
ADD="$WORK/add.json"
BUMP="$WORK/bump.json"
MATERIALIZED="$WORK/materialized.json"
SERIALIZED="$WORK/elixir-serialized.json"
trap 'rm -f "$ADD" "$BUMP" "$MATERIALIZED" "$SERIALIZED"; rmdir "$WORK"' EXIT

# Author two overlays with gsp itself.
"$GSP" add swarmidx:jmlago/strict-reviewer@2.0.1 --as agent:reviewer \
  --model openrouter:anthropic/claude --backend bwrap --swarm research -o "$ADD"
"$GSP" bump researcher --field body --from sha256:aaaa --to sha256:bbbb -o "$BUMP"

compare() {
  "$GSP" materialize "$@" > "$MATERIALIZED"
  # No pipeline or stderr suppression: either implementation's failure fails
  # the harness. Test config prevents automatic .env imports into this check.
  (cd "$GENSWARMS" && MIX_ENV=test mix run "$HERE/genswarms_fold.exs" "$MATERIALIZED" "$SERIALIZED" "$@")
  # Reparse and re-emit the Elixir-produced public JSON with the real Go CLI,
  # then compare that entire parsed state with the original expected fold.
  "$GSP" materialize "$SERIALIZED" > "$MATERIALIZED"
  (cd "$GENSWARMS" && MIX_ENV=test mix run "$HERE/genswarms_fold.exs" "$MATERIALIZED" "$SERIALIZED" "$@")
}

compare "$SEED" "$ADD" "$BUMP"
compare "$SEED" "$HERE/../examples/research/overlay.json"
compare "$HERE/../examples/execution/seed.json"
compare "$HERE/../examples/execution/seed.json" "$HERE/../examples/execution/overlay.json"
