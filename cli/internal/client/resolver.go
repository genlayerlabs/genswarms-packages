package client

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/genlayerlabs/genswarms-packages/cli/internal/manifest"
)

// ResolvedRelease contains only notarized release metadata. Module/card fields
// are intentionally absent: entry points come from the verified package bytes.
// LogSeq is an unsigned locator, not a trust anchor or freshness checkpoint.
type ResolvedRelease struct {
	Ref    string   `json:"ref"`
	Digest string   `json:"digest"`
	Source string   `json:"source"`
	Dir    string   `json:"dir"`
	Kind   string   `json:"kind"`
	Deps   []string `json:"deps"`
	LogSeq int64    `json:"log_seq"`
}

// Resolver is a verified snapshot of the returned log, not a freshness proof.
// It never reads the unauthenticated /resolve endpoint or mutates the notary.
type Resolver struct{ releases map[string]ResolvedRelease }

var releaseRef = regexp.MustCompile(`^swarmidx:[a-z0-9][a-z0-9-]*/[a-z0-9][a-z0-9._-]*@[0-9A-Za-z][0-9A-Za-z.+-]*$`)
var packageRef = regexp.MustCompile(`^swarmidx:[a-z0-9][a-z0-9-]*/[a-z0-9][a-z0-9._-]*$`)
var sha256Digest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func (c *Client) VerifiedResolver(pub ed25519.PublicKey) (*Resolver, error) {
	if len(pub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("resolution requires an independently trusted Ed25519 public key")
	}
	entries, err := c.FullLog()
	if err != nil {
		return nil, err
	}
	if ok, n := VerifyChain(entries, pub); !ok {
		return nil, fmt.Errorf("transparency log FAILED verification at entry index %d", n)
	}
	r := &Resolver{releases: make(map[string]ResolvedRelease)}
	for i, e := range entries {
		if err := r.apply(e); err != nil {
			return nil, fmt.Errorf("invalid signed log entry at index %d: %w", i, err)
		}
	}
	return r, nil
}

func (r *Resolver) Resolve(ref string) (ResolvedRelease, error) {
	if !releaseRef.MatchString(ref) {
		return ResolvedRelease{}, fmt.Errorf("expected an exact swarmidx:scope/name@version ref")
	}
	rel, ok := r.releases[ref]
	if !ok {
		return ResolvedRelease{}, fmt.Errorf("release absent or withdrawn in verified log: %s", ref)
	}
	// Do not let a caller modify the snapshot through the dependency slice.
	rel.Deps = append([]string{}, rel.Deps...)
	return rel, nil
}

func (r *Resolver) apply(e LogEntry) error {
	op, present := e.Payload["op"]
	if present {
		if op != "delete" {
			return fmt.Errorf("unsupported log operation")
		}
		ref, ok := e.Payload["ref"].(string)
		if !ok || !packageRef.MatchString(ref) {
			return fmt.Errorf("invalid withdrawal ref")
		}
		versions, ok := e.Payload["releases"].([]any)
		if !ok || len(versions) == 0 {
			return fmt.Errorf("invalid withdrawal releases")
		}
		withdrawn := map[string]bool{}
		for _, v := range versions {
			version, ok := v.(string)
			full := ref + "@" + version
			if !ok || !releaseRef.MatchString(full) || withdrawn[full] {
				return fmt.Errorf("invalid withdrawal version")
			}
			if _, ok := r.releases[full]; !ok {
				return fmt.Errorf("withdrawal of absent release")
			}
			withdrawn[full] = true
		}
		for full := range r.releases {
			if strings.HasPrefix(full, ref+"@") && !withdrawn[full] {
				return fmt.Errorf("incomplete package withdrawal")
			}
		}
		for full := range withdrawn {
			delete(r.releases, full)
		}
		return nil
	}
	data, err := json.Marshal(e.Payload)
	if err != nil {
		return fmt.Errorf("invalid release payload")
	}
	var rel ResolvedRelease
	if err := json.Unmarshal(data, &rel); err != nil {
		return fmt.Errorf("invalid release field types")
	}
	if !releaseRef.MatchString(rel.Ref) || !sha256Digest.MatchString(rel.Digest) || rel.Source == "" || rel.Dir == "" || !manifest.ValidKinds[rel.Kind] {
		return fmt.Errorf("invalid release metadata")
	}
	if _, present := e.Payload["deps"]; present && rel.Deps == nil {
		return fmt.Errorf("deps must be an array")
	}
	for _, dep := range rel.Deps {
		if _, ok := r.releases["swarmidx:"+dep]; !ok {
			return fmt.Errorf("dependency absent from preceding log")
		}
	}
	if _, exists := r.releases[rel.Ref]; exists {
		return fmt.Errorf("duplicate active release")
	}
	rel.LogSeq = e.Seq
	r.releases[rel.Ref] = rel
	return nil
}
