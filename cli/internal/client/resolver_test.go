package client

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func releasePayload(name string) map[string]any {
	return map[string]any{"ref": "swarmidx:fixture/" + name + "@1", "digest": "sha256:" + strings.Repeat("a", 64),
		"source": "github://fixture/packages@v1", "dir": name, "kind": "body", "deps": []any{}}
}

func resolverFixture(t *testing.T, payloads []map[string]any, tamper func([]LogEntry)) (*Client, ed25519.PublicKey) {
	t.Helper()
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	entries := make([]LogEntry, 0, len(payloads))
	prev := ""
	for i, p := range payloads {
		data, err := canonical(p)
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(append([]byte(prev), data...))
		e := LogEntry{Seq: int64(i*2 + 1), Payload: p, PrevHash: prev, EntryHash: hex.EncodeToString(hash[:]), Signature: hex.EncodeToString(ed25519.Sign(key, hash[:]))}
		entries = append(entries, e)
		prev = e.EntryHash
	}
	if tamper != nil {
		tamper(entries)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/log" {
			t.Error("resolver contacted unauthenticated metadata/key endpoint")
			http.Error(w, "unexpected", 500)
			return
		}
		since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
		page := []LogEntry{}
		for _, e := range entries {
			if e.Seq > since {
				page = append(page, e)
				break
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"entries": page})
	}))
	t.Cleanup(server.Close)
	return New(server.URL, ""), key.Public().(ed25519.PublicKey)
}

func TestVerifiedResolverUsesSignedMetadataAndDependencies(t *testing.T) {
	a, b := releasePayload("a"), releasePayload("b")
	delete(a, "deps") // Older publications predate the deps field.
	b["deps"] = []any{"fixture/a@1"}
	b["module"] = "UntrustedMirrorMustNotBeExposed"
	c, pub := resolverFixture(t, []map[string]any{a, b}, nil)
	r, err := c.VerifiedResolver(pub)
	if err != nil {
		t.Fatal(err)
	}
	rel, err := r.Resolve("swarmidx:fixture/b@1")
	if err != nil || rel.Digest != b["digest"] || rel.Source != b["source"] || rel.Dir != "b" || rel.Kind != "body" || len(rel.Deps) != 1 || rel.LogSeq != 3 {
		t.Fatalf("bad release: %+v %v", rel, err)
	}
	data, _ := json.Marshal(rel)
	if strings.Contains(string(data), "module") {
		t.Fatal("unsigned mirror exposed as authenticated metadata")
	}
	rel.Deps[0] = "changed"
	again, _ := r.Resolve(rel.Ref)
	if again.Deps[0] != "fixture/a@1" {
		t.Fatal("caller mutated verified snapshot")
	}
}

func TestVerifiedResolverRejectsTamperedMetadata(t *testing.T) {
	for field, value := range map[string]any{"digest": "sha256:" + strings.Repeat("b", 64), "source": "local:/unexpected", "dir": "other", "kind": "handler", "ref": "swarmidx:fixture/other@1", "deps": []any{"fixture/other@1"}} {
		t.Run(field, func(t *testing.T) {
			c, pub := resolverFixture(t, []map[string]any{releasePayload("a")}, func(e []LogEntry) { e[0].Payload[field] = value })
			if _, err := c.VerifiedResolver(pub); err == nil {
				t.Fatal("tampered signed field accepted")
			}
		})
	}
}

func TestVerifiedResolverRejectsWrongTrustAnchor(t *testing.T) {
	c, _ := resolverFixture(t, []map[string]any{releasePayload("a")}, nil)
	other := ed25519.NewKeyFromSeed([]byte(strings.Repeat("x", 32))).Public().(ed25519.PublicKey)
	if _, err := c.VerifiedResolver(other); err == nil {
		t.Fatal("wrong signer accepted")
	}
	if _, err := New(":invalid", "").VerifiedResolver(nil); err == nil || !strings.Contains(err.Error(), "trusted") {
		t.Fatal("missing key reached network")
	}
}

func TestVerifiedResolverWithdrawalAndRepublication(t *testing.T) {
	withdrawal := map[string]any{"op": "delete", "ref": "swarmidx:fixture/a", "releases": []any{"1"}, "by": "fixture"}
	for _, republish := range []bool{false, true} {
		payloads := []map[string]any{releasePayload("a"), withdrawal}
		if republish {
			p := releasePayload("a")
			p["digest"] = "sha256:" + strings.Repeat("b", 64)
			payloads = append(payloads, p)
		}
		c, pub := resolverFixture(t, payloads, nil)
		r, err := c.VerifiedResolver(pub)
		if err != nil {
			t.Fatal(err)
		}
		rel, err := r.Resolve("swarmidx:fixture/a@1")
		if republish {
			if err != nil || rel.Digest != "sha256:"+strings.Repeat("b", 64) {
				t.Fatal("republication did not replace withdrawn generation")
			}
		} else if err == nil {
			t.Fatal("withdrawn release resolved")
		}
	}
}

func TestVerifiedResolverRejectsInvalidSignedSemantics(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"unknown op":     func(p map[string]any) { p["op"] = "replace" },
		"missing digest": func(p map[string]any) { delete(p, "digest") },
		"unsafe ref":     func(p map[string]any) { p["ref"] = "swarmidx:fixture/a@../../outside" },
		"invalid kind":   func(p map[string]any) { p["kind"] = "app" },
		"null deps":      func(p map[string]any) { p["deps"] = nil },
		"invalid deps":   func(p map[string]any) { p["deps"] = []any{7} },
		"future dep":     func(p map[string]any) { p["deps"] = []any{"fixture/later@1"} },
		"unknown withdrawal": func(p map[string]any) {
			p["op"] = "delete"
			p["ref"] = "swarmidx:fixture/a"
			p["releases"] = []any{"1"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := releasePayload("a")
			mutate(p)
			c, pub := resolverFixture(t, []map[string]any{p}, nil)
			if _, err := c.VerifiedResolver(pub); err == nil {
				t.Fatal("invalid signed semantics accepted")
			}
		})
	}
	c, pub := resolverFixture(t, []map[string]any{releasePayload("a"), releasePayload("a")}, nil)
	if _, err := c.VerifiedResolver(pub); err == nil {
		t.Fatal("active immutable release silently overwritten")
	}
}
