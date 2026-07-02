package ir

// ResolveSwarmidx fills the digest of every authored `swarmidx:` ref (one lacking
// a digest) by calling `resolve(ref)` — the notary's name→digest function. Refs
// that already carry a digest, and non-swarmidx refs (model endpoints, backends),
// are left untouched. This is the "resolve" half of `gsp materialize` (§11).
func (s *State) ResolveSwarmidx(resolve func(ref string) (string, error)) error {
	for i := range s.Agents {
		if err := resolveRef(&s.Agents[i].Body, resolve); err != nil {
			return err
		}
		if err := resolveRef(&s.Agents[i].Model.Ref, resolve); err != nil {
			return err
		}
	}
	for i := range s.Objects {
		if err := resolveRef(&s.Objects[i].Handler, resolve); err != nil {
			return err
		}
	}
	return nil
}

func resolveRef(r *Ref, resolve func(string) (string, error)) error {
	if r.Scheme != "swarmidx" || r.Digest != "" {
		return nil
	}
	digest, err := resolve(r.Ref)
	if err != nil {
		return err
	}
	r.Digest = digest
	return nil
}

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
