// Command gsp is the GenSwarms Packages CLI — the offline authoring plane
// (design §11). It resolves, folds, hashes and validates IR deterministically
// and never touches a live swarm (that is the genswarms control plane, §4).
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/genlayerlabs/genswarms-packages/cli/internal/dirhash"
	"github.com/genlayerlabs/genswarms-packages/cli/internal/ir"
	"github.com/genlayerlabs/genswarms-packages/cli/internal/manifest"
)

const usage = `gsp — GenSwarms Packages CLI (offline authoring plane)

usage:
  gsp dirhash <dir>                   reproducible digest of a package dir (sha256:…)
  gsp materialize <seed> [overlay…]   fold overlays onto a seed → materialized swarm.state
  gsp verify <ir.json>                parse + validate a swarm.state or swarm.overlay
  gsp manifest <swarmidx.json>        parse + validate a package manifest

The offline plane is deterministic and never touches a live swarm (that is the
genswarms control plane). Output is on stdout; errors go to stderr.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "dirhash":
		err = cmdDirhash(os.Args[2:])
	case "materialize":
		err = cmdMaterialize(os.Args[2:])
	case "verify":
		err = cmdVerify(os.Args[2:])
	case "manifest":
		err = cmdManifest(os.Args[2:])
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "gsp: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "gsp: %v\n", err)
		os.Exit(1)
	}
}

func cmdDirhash(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: gsp dirhash <dir>")
	}
	h, err := dirhash.HashDir(args[0])
	if err != nil {
		return err
	}
	fmt.Println(h)
	return nil
}

func cmdMaterialize(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: gsp materialize <seed> [overlay…]")
	}
	seedData, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	state, err := ir.ParseState(seedData)
	if err != nil {
		return err
	}
	for _, ov := range args[1:] {
		data, err := os.ReadFile(ov)
		if err != nil {
			return err
		}
		overlay, err := ir.ParseOverlay(data)
		if err != nil {
			return err
		}
		state, err = ir.Fold(state, overlay.Events)
		if err != nil {
			return err
		}
	}
	out, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	return nil
}

func cmdVerify(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: gsp verify <ir.json>")
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	var probe map[string]any
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	switch probe["kind"] {
	case "swarm.state":
		state, err := ir.ParseState(data)
		if err != nil {
			return err
		}
		resolved := state.ValidateResolved() == nil
		fmt.Printf("ok: swarm.state %q — %d agents, %d objects, resolved=%v\n",
			state.Name, len(state.Agents), len(state.Objects), resolved)
		return nil
	case "swarm.overlay":
		overlay, err := ir.ParseOverlay(data)
		if err != nil {
			return err
		}
		fmt.Printf("ok: swarm.overlay %q — %d events\n", overlay.Swarm, len(overlay.Events))
		return nil
	default:
		return fmt.Errorf("unknown IR kind: %v", probe["kind"])
	}
}

func cmdManifest(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: gsp manifest <swarmidx.json>")
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	m, err := manifest.Parse(data)
	if err != nil {
		return err
	}
	fmt.Printf("ok: scope %q — %d packages\n", m.Registry.Scope, len(m.Packages))
	for _, p := range m.Packages {
		fmt.Printf("  %s/%s  [%s]  %s\n", m.Registry.Scope, p.Name, p.Kind, p.Dir)
	}
	return nil
}
