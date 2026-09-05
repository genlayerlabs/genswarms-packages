# Compare every parsed IR field, not a hand-picked summary. This checks semantic
# equality; JSON whitespace and omitted default fields are not byte equality.
[materialized, serialized, seed | overlays] = System.argv()

parse_state = fn path ->
  {:ok, state} = path |> File.read!() |> Jason.decode!() |> Genswarms.IR.State.parse()
  state
end

expected =
  Enum.reduce(overlays, parse_state.(seed), fn path, state ->
    {:ok, overlay} = path |> File.read!() |> Jason.decode!() |> Genswarms.IR.Overlay.parse()
    {:ok, next} = Genswarms.IR.Fold.fold(state, overlay)
    next
  end)

actual = parse_state.(materialized)

if actual !== expected do
  # Inputs may contain private configuration; do not dump their values.
  raise "CONFORMANCE FAILED: complete parsed Go and Elixir states differ for #{Path.basename(seed)}"
end

document = expected |> Genswarms.IR.State.to_map() |> Jason.encode!()
{:ok, round_trip} = document |> Jason.decode!() |> Genswarms.IR.State.parse()
if round_trip !== expected, do: raise("CONFORMANCE FAILED: Elixir JSON serialization loses state")
File.write!(serialized, document)

IO.puts("CONFORMANCE OK: complete parsed state for #{Path.basename(seed)}")
