# Fold a seed + overlays with genswarms' REAL Elixir IR and print a summary, for
# cross-impl conformance against the gsp CLI (Go). Run from a genswarms checkout:
#
#   mix run conformance/genswarms_fold.exs <seed.json> <overlay.json> ...
#
# (see conformance/run.sh, which drives both sides and diffs them.)
[seed | overlays] = System.argv()

{:ok, state0} = seed |> File.read!() |> Jason.decode!() |> Genswarms.IR.State.parse()

state =
  Enum.reduce(overlays, state0, fn path, s ->
    {:ok, ov} = path |> File.read!() |> Jason.decode!() |> Genswarms.IR.Overlay.parse()
    {:ok, s2} = Genswarms.IR.Fold.fold(s, ov)
    s2
  end)

names = state.agents |> Enum.map(& &1.name) |> Enum.join(",")
r = Enum.find(state.agents, &(&1.name == "researcher"))
topo = state.topology |> Enum.map(fn {f, t} -> f <> ">" <> t end) |> Enum.join(" ")

IO.puts("agents=" <> names)
IO.puts("researcher_body_digest=" <> (r && r.body.digest || ""))
IO.puts("topology=" <> topo)
