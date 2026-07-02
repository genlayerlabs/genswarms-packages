// Command gsp is the GenSwarms Packages CLI. It has an offline authoring plane
// (dirhash / materialize / verify / manifest — deterministic, no network) and a
// notary client (publish / resolve / log) that talks to a swarmidx service.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/genlayerlabs/genswarms-packages/cli/internal/client"
	"github.com/genlayerlabs/genswarms-packages/cli/internal/dirhash"
	"github.com/genlayerlabs/genswarms-packages/cli/internal/ir"
	"github.com/genlayerlabs/genswarms-packages/cli/internal/manifest"
)

const usage = `gsp — GenSwarms Packages CLI

offline authoring plane (deterministic, no network):
  gsp dirhash <dir>                   reproducible digest of a package dir (sha256:…)
  gsp materialize <seed> [overlay…]   fold overlays onto a seed → materialized swarm.state
  gsp verify <ir.json>                parse + validate a swarm.state or swarm.overlay
  gsp manifest <swarmidx.json>        parse + validate a package manifest

authoring (emit a one-event swarm.overlay to stdout or -o file):
  gsp add <pkgref> --as agent:NAME --model REF --backend REF [--swarm S]   add_agent
  gsp add <pkgref> --as object:NAME [--swarm S]                            add_object
  gsp bump <target> --field F --from DIGEST --to DIGEST [--migration P]    bump_package

notary client (talks to swarmidx; --endpoint / $SWARMIDX_ENDPOINT, --token / $SWARMIDX_TOKEN):
  gsp publish <swarmidx.json> --version V [--source S]   dirhash each package dir and publish it
  gsp resolve <ref>                                      resolve swarmidx:scope/name@version → digest
  gsp log [--since N]                                    fetch + verify the transparency log (Ed25519)
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
	case "add":
		err = cmdAdd(os.Args[2:])
	case "bump":
		err = cmdBump(os.Args[2:])
	case "publish":
		err = cmdPublish(os.Args[2:])
	case "resolve":
		err = cmdResolve(os.Args[2:])
	case "log":
		err = cmdLog(os.Args[2:])
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

// ── offline plane ────────────────────────────────────────────────────────────

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
	fs := flag.NewFlagSet("materialize", flag.ContinueOnError)
	doResolve := fs.Bool("resolve", false, "resolve swarmidx: refs against the notary (fills digests)")
	endpoint := endpointFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	files := fs.Args()
	if len(files) < 1 {
		return fmt.Errorf("usage: gsp materialize [--resolve] <seed> [overlay…]")
	}
	seedData, err := os.ReadFile(files[0])
	if err != nil {
		return err
	}
	state, err := ir.ParseState(seedData)
	if err != nil {
		return err
	}
	for _, ov := range files[1:] {
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
	if *doResolve {
		c := client.New(*endpoint, "")
		if err := state.ResolveSwarmidx(func(ref string) (string, error) {
			out, err := c.Resolve(ref)
			if err != nil {
				return "", err
			}
			d, _ := out["digest"].(string)
			if d == "" {
				return "", fmt.Errorf("no digest for %s", ref)
			}
			return d, nil
		}); err != nil {
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

// ── notary client ────────────────────────────────────────────────────────────

func endpointFlag(fs *flag.FlagSet) *string {
	return fs.String("endpoint", env("SWARMIDX_ENDPOINT", "http://localhost:8000"), "swarmidx endpoint")
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func cmdPublish(args []string) error {
	fs := flag.NewFlagSet("publish", flag.ContinueOnError)
	endpoint := endpointFlag(fs)
	token := fs.String("token", os.Getenv("SWARMIDX_TOKEN"), "publish token (or $SWARMIDX_TOKEN)")
	version := fs.String("version", "", "version to publish (the tag, e.g. 1.2.3)")
	source := fs.String("source", "", "source ref, e.g. github://owner/repo@v1.2.3")
	if len(args) < 1 {
		return fmt.Errorf("usage: gsp publish <swarmidx.json> --version V [--source S]")
	}
	manifestPath := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *version == "" {
		return fmt.Errorf("--version is required")
	}
	if *token == "" {
		return fmt.Errorf("a publish token is required (--token or $SWARMIDX_TOKEN)")
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	m, err := manifest.Parse(data)
	if err != nil {
		return err
	}
	root := filepath.Dir(manifestPath)
	c := client.New(*endpoint, *token)
	for _, p := range m.Packages {
		digest, err := dirhash.HashDir(filepath.Join(root, p.Dir))
		if err != nil {
			return fmt.Errorf("%s: %w", p.Name, err)
		}
		out, err := c.Publish(client.Release{
			Name: p.Name, Kind: p.Kind, Version: *version, Digest: digest, Source: *source, Dir: p.Dir,
			Description: p.Description, Docs: p.Docs, Skill: p.Skill,
		})
		if err != nil {
			return fmt.Errorf("publish %s: %w", p.Name, err)
		}
		fmt.Printf("published %v  %s  (log #%v)\n", out["ref"], digest, out["log_seq"])
	}
	return nil
}

func cmdResolve(args []string) error {
	fs := flag.NewFlagSet("resolve", flag.ContinueOnError)
	endpoint := endpointFlag(fs)
	asJSON := fs.Bool("json", false, "print the raw JSON response")
	if len(args) < 1 {
		return fmt.Errorf("usage: gsp resolve <ref> [--json]")
	}
	ref := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	out, err := client.New(*endpoint, "").Resolve(ref)
	if err != nil {
		return err
	}
	if *asJSON {
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	fmt.Printf("%v\n  digest:  %v\n  source:  %v\n  kind:    %v\n  log #:   %v\n",
		out["ref"], out["digest"], out["source"], out["kind"], out["log_seq"])
	return nil
}

func cmdLog(args []string) error {
	fs := flag.NewFlagSet("log", flag.ContinueOnError)
	endpoint := endpointFlag(fs)
	since := fs.Int("since", 0, "only entries with seq > since")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c := client.New(*endpoint, "")
	pub, err := c.PublicKey()
	if err != nil {
		return err
	}
	entries, err := c.Log(*since)
	if err != nil {
		return err
	}
	ok, n := client.VerifyChain(entries, pub)
	for _, e := range entries {
		fmt.Printf("#%d  %v\n", e.Seq, e.Payload["ref"])
	}
	if !ok {
		return fmt.Errorf("transparency log FAILED verification at entry index %d", n)
	}
	fmt.Printf("log verified: %d entries, hash chain + Ed25519 signatures OK\n", n)
	return nil
}

// ── authoring (emit overlays) ────────────────────────────────────────────────

func refMapJSON(ref, kind string) (map[string]any, error) {
	if _, err := ir.SchemeOf(ref); err != nil {
		return nil, err
	}
	m := map[string]any{"ref": ref}
	if kind != "" {
		m["kind"] = kind
	}
	return m, nil
}

func modelMapJSON(ref string) (map[string]any, error) {
	scheme, err := ir.SchemeOf(ref)
	if err != nil {
		return nil, err
	}
	if scheme == "swarmidx" { // a policy ref
		return map[string]any{"policy": map[string]any{"ref": ref, "kind": "data"}}, nil
	}
	return map[string]any{"ref": ref}, nil // a service ref (no kind)
}

func emitOverlay(swarm string, seq int, op string, payload map[string]any, outPath string) error {
	overlay := map[string]any{
		"v": 1, "kind": "swarm.overlay", "swarm": swarm, "apply": "incremental",
		"events": []any{map[string]any{"seq": seq, "op": op, "payload": payload}},
	}
	data, err := json.MarshalIndent(overlay, "", "  ")
	if err != nil {
		return err
	}
	// Self-check: the emitted overlay must be valid IR2.
	if _, err := ir.ParseOverlay(data); err != nil {
		return fmt.Errorf("emitted an invalid overlay: %w", err)
	}
	if outPath != "" {
		return os.WriteFile(outPath, append(data, '\n'), 0o644)
	}
	fmt.Println(string(data))
	return nil
}

func cmdAdd(args []string) error {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	as := fs.String("as", "", "slot: agent:NAME or object:NAME")
	model := fs.String("model", "", "model ref (for agent): a service ref or a swarmidx policy")
	backend := fs.String("backend", "bwrap", "backend ref (for agent)")
	swarm := fs.String("swarm", "swarm", "the swarm name for the overlay envelope")
	seq := fs.Int("seq", 1, "event seq")
	out := fs.String("o", "", "write the overlay to this file instead of stdout")
	if len(args) < 1 {
		return fmt.Errorf("usage: gsp add <pkgref> --as agent:NAME|object:NAME [flags]")
	}
	pkgref := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	slotType, name, ok := strings.Cut(*as, ":")
	if !ok || name == "" {
		return fmt.Errorf("--as must be agent:NAME or object:NAME")
	}
	switch slotType {
	case "object":
		handler, err := refMapJSON(pkgref, "code")
		if err != nil {
			return err
		}
		return emitOverlay(*swarm, *seq, "add_object",
			map[string]any{"name": name, "handler": handler}, *out)
	case "agent":
		if *model == "" {
			return fmt.Errorf("--model is required for an agent")
		}
		body, err := refMapJSON(pkgref, "data")
		if err != nil {
			return err
		}
		mdl, err := modelMapJSON(*model)
		if err != nil {
			return err
		}
		bk, err := refMapJSON(*backend, "")
		if err != nil {
			return err
		}
		return emitOverlay(*swarm, *seq, "add_agent",
			map[string]any{"name": name, "body": body, "model": mdl, "backend": bk}, *out)
	default:
		return fmt.Errorf("--as must be agent:NAME or object:NAME (got %q)", slotType)
	}
}

func cmdBump(args []string) error {
	fs := flag.NewFlagSet("bump", flag.ContinueOnError)
	field := fs.String("field", "", "body | model | backend | handler")
	from := fs.String("from", "", "current digest (sha256:…)")
	to := fs.String("to", "", "new digest (sha256:…)")
	migration := fs.String("migration", "", "state_migrate | restart")
	swarm := fs.String("swarm", "swarm", "the swarm name for the overlay envelope")
	seq := fs.Int("seq", 1, "event seq")
	out := fs.String("o", "", "write the overlay to this file instead of stdout")
	if len(args) < 1 {
		return fmt.Errorf("usage: gsp bump <target> --field F --from DIGEST --to DIGEST")
	}
	target := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *field == "" || *from == "" || *to == "" {
		return fmt.Errorf("--field, --from and --to are required")
	}
	payload := map[string]any{"target": target, "field": *field, "from": *from, "to": *to}
	if *migration != "" {
		payload["migration"] = *migration
	}
	return emitOverlay(*swarm, *seq, "bump_package", payload, *out)
}
