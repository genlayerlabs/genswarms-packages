package ir

import (
	"fmt"
	"regexp"
	"strings"
)

// Ref is a reference in the GenSwarms IR (IR spec §2): the unit a resolver and
// the transparency log operate on. Go mirror of Genswarms.IR.Ref. The canonical
// serialization is JSON with string keys.
//
// Two orthogonal axes classify a ref: who resolves it (swarmidx vs external) and
// whether it is content-addressable (swarmidx/oci carry a digest). A ref exists
// in two forms of the same shape: authored (may omit digest) and resolved
// (content-addressable refs carry an inline digest).
type Ref struct {
	Ref      string // the ref string, e.g. "swarmidx:jmlago/coder@0.4.0"
	Scheme   string // derived: the part before the first ':'
	Digest   string // "" if absent
	Kind     string // "data" | "code" | "" (omitted for non-package refs)
	Attested bool
	Host     string
}

var (
	contentAddressableSchemes = map[string]bool{"swarmidx": true, "oci": true}
	hostRequiredSchemes       = map[string]bool{"ssh": true}
	bareSchemes               = map[string]bool{"ssh": true, "host": true, "local": true, "bwrap": true, "mock": true}
	digestRe                  = regexp.MustCompile(`^[a-z0-9]+:[0-9a-f]+$`)
)

// ContentAddressable reports whether a scheme's content is addressed by a digest.
func ContentAddressable(scheme string) bool { return contentAddressableSchemes[scheme] }

// ValidDigest reports whether d is "<algo>:<hex>" (e.g. "sha256:9f2c…").
func ValidDigest(d string) bool { return digestRe.MatchString(d) }

// SchemeOf extracts the scheme (the part before the first ':'). A bare scheme
// (no ':body') is valid only for connection-style schemes.
func SchemeOf(ref string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("invalid ref string")
	}
	parts := strings.SplitN(ref, ":", 2)
	if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
		return parts[0], nil
	}
	if len(parts) == 1 && bareSchemes[parts[0]] {
		return parts[0], nil
	}
	return "", fmt.Errorf("invalid ref string: %q", ref)
}

// ParseRef parses a JSON-decoded ref map (string keys), authored-safe (accepts
// refs without a digest).
func ParseRef(m map[string]any) (Ref, error) {
	refStr, ok := m["ref"].(string)
	if !ok || refStr == "" {
		return Ref{}, fmt.Errorf("missing ref")
	}
	scheme, err := SchemeOf(refStr)
	if err != nil {
		return Ref{}, err
	}
	kind, err := refKind(m, scheme)
	if err != nil {
		return Ref{}, err
	}
	if err := refHost(scheme, m); err != nil {
		return Ref{}, err
	}
	attested, err := refAttested(m)
	if err != nil {
		return Ref{}, err
	}
	digest, _ := m["digest"].(string)
	host, _ := m["host"].(string)
	return Ref{Ref: refStr, Scheme: scheme, Digest: digest, Kind: kind, Attested: attested, Host: host}, nil
}

func refKind(m map[string]any, scheme string) (string, error) {
	switch k := m["kind"].(type) {
	case string:
		if k == "data" || k == "code" {
			return k, nil
		}
		return "", fmt.Errorf("invalid kind: %q", k)
	case nil:
		// kind describes package content: required for content-addressable refs,
		// omitted for non-package refs (model endpoints, ssh hosts).
		if ContentAddressable(scheme) {
			return "", fmt.Errorf("missing kind for content-addressable scheme %q", scheme)
		}
		return "", nil
	default:
		return "", fmt.Errorf("invalid kind: %v", m["kind"])
	}
}

func refHost(scheme string, m map[string]any) error {
	if !hostRequiredSchemes[scheme] {
		return nil
	}
	if h, ok := m["host"].(string); ok && h != "" {
		return nil
	}
	return fmt.Errorf("missing host for scheme %q", scheme)
}

func refAttested(m map[string]any) (bool, error) {
	switch v := m["attested"].(type) {
	case nil:
		return false, nil
	case bool:
		return v, nil
	default:
		return false, fmt.Errorf("invalid attested: %v", v)
	}
}

// ValidateResolved enforces the resolved-form digest rule (§2.3): a
// content-addressable ref MUST carry a well-formed digest; a non-hashable ref
// MUST NOT carry one.
func (r Ref) ValidateResolved() error {
	ca := ContentAddressable(r.Scheme)
	switch {
	case ca && r.Digest == "":
		return fmt.Errorf("missing digest for %q", r.Ref)
	case ca && !ValidDigest(r.Digest):
		return fmt.Errorf("invalid digest %q for %q", r.Digest, r.Ref)
	case !ca && r.Digest != "":
		return fmt.Errorf("unexpected digest on non-addressable ref %q", r.Ref)
	}
	return nil
}
