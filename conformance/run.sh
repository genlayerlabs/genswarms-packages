#!/usr/bin/env sh
# Cross-impl conformance: gsp (Go) fold == genswarms (Elixir) fold, on the same
# seed + overlays. The gsp IR is a second implementation of the genswarms IR; this
# proves it does not diverge.
#
# Requires: a genswarms checkout with mix, a built gsp, and jq.
#   GENSWARMS=/path/to/genswarms GSP=/path/to/gsp ./conformance/run.sh
set -eu

HERE="$(cd "$(dirname "$0")" && pwd)"
GENSWARMS="${GENSWARMS:-$HOME/docs/personal/genswarms}"
GSP="${GSP:-gsp}"
SEED="$HERE/../examples/research/seed.json"
ADD="$(mktemp)"; BUMP="$(mktemp)"
trap 'rm -f "$ADD" "$BUMP"' EXIT

# Author two overlays with gsp itself.
"$GSP" add swarmidx:jmlago/strict-reviewer@2.0.1 --as agent:reviewer \
  --model openrouter:anthropic/claude --backend bwrap --swarm research -o "$ADD"
"$GSP" bump researcher --field body --from sha256:aaaa --to sha256:bbbb -o "$BUMP"

# Elixir (genswarms real IR).
ELX="$(cd "$GENSWARMS" && mix run "$HERE/genswarms_fold.exs" "$SEED" "$ADD" "$BUMP" 2>/dev/null | grep -E '^(agents|researcher_body_digest|topology)=')"

# Go (gsp).
M="$("$GSP" materialize "$SEED" "$ADD" "$BUMP")"
GO="$(printf 'agents=%s\nresearcher_body_digest=%s\ntopology=%s\n' \
  "$(echo "$M" | jq -r '[.agents[].name]|join(",")')" \
  "$(echo "$M" | jq -r '.agents[]|select(.name=="researcher").body.digest')" \
  "$(echo "$M" | jq -r '[.topology[]|.[0]+">"+.[1]]|join(" ")')")"

echo "--- elixir ---"; echo "$ELX"
echo "--- go ---"; echo "$GO"
if [ "$ELX" = "$GO" ]; then
  echo "CONFORMANCE OK — gsp (Go) == genswarms (Elixir)"
else
  echo "CONFORMANCE FAILED — the two IR implementations diverge"; exit 1
fi
