package ir

// SwarmidxRefs returns every swarmidx: ref in the state (resolved or not),
// deduplicated in first-appearance order — the ref set `gsp vendor` fetches.
func (s *State) SwarmidxRefs() []string {
	seen := map[string]bool{}
	var out []string
	add := func(r Ref) {
		if r.Scheme == "swarmidx" && !seen[r.Ref] {
			seen[r.Ref] = true
			out = append(out, r.Ref)
		}
	}
	for _, a := range s.Agents {
		add(a.Body)
		add(a.Model.Ref)
	}
	for _, o := range s.Objects {
		add(o.Handler)
	}
	return out
}
