package client

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestLogRejectsMalformedResponses(t *testing.T) {
	for name, body := range map[string]string{
		"invalid JSON":    "not JSON",
		"empty":           "",
		"missing entries": `{}`,
		"null entries":    `{"entries":null}`,
		"wrong entries":   `{"entries":{}}`,
		"trailing JSON":   `{"entries":[]} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(body))
			}))
			defer server.Close()
			if _, err := New(server.URL, "").Log(0); err == nil {
				t.Fatal("malformed response accepted as a log")
			}
		})
	}
}

func TestFullLogPagination(t *testing.T) {
	for _, badSecondPage := range []bool{false, true} {
		t.Run(strconv.FormatBool(badSecondPage), func(t *testing.T) {
			entries, pub := signedEntries(t)
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				since, _ := strconv.Atoi(r.URL.Query().Get("since"))
				page := []LogEntry{}
				if since < len(entries) {
					e := entries[since]
					if badSecondPage && since == 1 {
						e.Signature = "00"
					}
					page = append(page, e)
				}
				json.NewEncoder(w).Encode(map[string]any{"entries": page})
			}))
			defer server.Close()
			got, err := New(server.URL, "").FullLog()
			if err != nil || len(got) != 2 || calls != 3 {
				t.Fatalf("pagination: entries=%d calls=%d err=%v", len(got), calls, err)
			}
			if ok, _ := VerifyChain(got, pub); ok == badSecondPage {
				t.Fatalf("verification ignored second page: ok=%v", ok)
			}
		})
	}
}

func TestFullLogRejectsNonProgressingPage(t *testing.T) {
	entries, _ := signedEntries(t)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		json.NewEncoder(w).Encode(map[string]any{"entries": entries[:1]})
	}))
	defer server.Close()
	if _, err := New(server.URL, "").FullLog(); err == nil || calls != 2 {
		t.Fatalf("repeated page must fail promptly: calls=%d err=%v", calls, err)
	}
}

func TestPythonCanonicalUnicode(t *testing.T) {
	// Produced by registry.transparency.canonical (Python's ensure_ascii=True).
	want := `{"deps":[],"dir":"caf\u00e9/\ud83d\ude80","ref":"swarmidx:fixture/body@1.0.0"}`
	payload := map[string]any{"deps": []any{}, "dir": "café/🚀", "ref": "swarmidx:fixture/body@1.0.0"}
	got, err := canonical(payload)
	if err != nil || string(got) != want {
		t.Fatalf("canonical bytes differ: got %q, want %q, err=%v", got, want, err)
	}
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)) // public test fixture only
	hash := sha256.Sum256([]byte(want))
	entry := LogEntry{Seq: 1, Payload: payload, EntryHash: hex.EncodeToString(hash[:]),
		Signature: hex.EncodeToString(ed25519.Sign(key, hash[:]))}
	if entry.EntryHash != "e3b17c9c9d3eac8fdbe991752b51bf9d05c84b649915bbe4bce1fa34a23c7a27" ||
		entry.Signature != "2f7c857493ad6bb50d612b472d6be93ea34530317d293a5c332432181e95e3b3f400a253ebadbf5cf34de14f177120aa84c0166d173f8e54bfc28aed0a83a30d" {
		t.Fatal("hash/signature differs from Python golden vector")
	}
	if ok, _ := VerifyChain([]LogEntry{entry}, key.Public().(ed25519.PublicKey)); !ok {
		t.Fatal("valid Python-canonicalized signature rejected")
	}
}

func TestPythonCanonicalEscapes(t *testing.T) {
	for input, want := range map[string]string{
		"\x7f":           `{"text":"\u007f"}`,
		"\b\t\n\f\r\x00": `{"text":"\b\t\n\f\r\u0000"}`,
		"<>&/\"\\":       `{"text":"<>&/\"\\"}`,
		"\u2028\u2029":   `{"text":"\u2028\u2029"}`,
	} {
		got, err := canonical(map[string]any{"text": input})
		if err != nil || string(got) != want {
			t.Errorf("canonical(%q)=%q, want %q, err=%v", input, got, want, err)
		}
	}
}

func signedEntries(t *testing.T) ([]LogEntry, ed25519.PublicKey) {
	t.Helper()
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	entries := []LogEntry{}
	prev := ""
	for i := 1; i <= 2; i++ {
		payload := map[string]any{"ref": "swarmidx:fixture/body@1.0.0", "version_index": i}
		data, _ := canonical(payload)
		hash := sha256.Sum256(append([]byte(prev), data...))
		entry := LogEntry{Seq: int64(i), Payload: payload, PrevHash: prev,
			EntryHash: hex.EncodeToString(hash[:]), Signature: hex.EncodeToString(ed25519.Sign(key, hash[:]))}
		entries = append(entries, entry)
		prev = entry.EntryHash
	}
	return entries, key.Public().(ed25519.PublicKey)
}

func TestVerifyChainRejectsTampering(t *testing.T) {
	for name, mutate := range map[string]func([]LogEntry){
		"payload":            func(e []LogEntry) { e[0].Payload["ref"] = "swarmidx:other/body@1.0.0" },
		"signature":          func(e []LogEntry) { e[0].Signature = "00" },
		"previous hash":      func(e []LogEntry) { e[1].PrevHash = "wrong" },
		"reorder":            func(e []LogEntry) { e[0], e[1] = e[1], e[0] },
		"duplicate sequence": func(e []LogEntry) { e[1].Seq = e[0].Seq },
		"invalid sequence":   func(e []LogEntry) { e[0].Seq = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			entries, pub := signedEntries(t)
			mutate(entries)
			if ok, _ := VerifyChain(entries, pub); ok {
				t.Fatal("tampered chain accepted")
			}
		})
	}
}

func TestVerifyChainRejectsInvalidKeyWithoutPanicking(t *testing.T) {
	entries, _ := signedEntries(t)
	if ok, _ := VerifyChain(entries, nil); ok {
		t.Fatal("invalid key accepted")
	}
}

func TestLogPreservesJSONNumberPrecision(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"entries":[{"seq":1,"payload":{"count":9007199254740993,"value":1.0}}]}`))
	}))
	defer server.Close()
	entries, err := New(server.URL, "").Log(0)
	if err != nil {
		t.Fatal(err)
	}
	if entries[0].Payload["count"] != json.Number("9007199254740993") || entries[0].Payload["value"] != json.Number("1.0") {
		t.Fatal("signed payload numbers were changed during decoding")
	}
}

func TestHTTPResponseBoundsAndRedaction(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
		want   string
	}{
		"oversized":    {200, strings.Repeat(" ", (4<<20)+1), "exceeds 4 MiB"},
		"opaque error": {403, "private-response-fixture", "HTTP 403"},
		"null":         {200, "null", "invalid JSON object"},
		"array":        {200, "[]", "invalid JSON object"},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer server.Close()
			_, err := New(server.URL, "").Resolve("swarmidx:fixture/body@1.0.0")
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "private-response-fixture") {
				t.Fatalf("unexpected response error: %v", err)
			}
		})
	}
}

func TestInvalidNotaryURLReturnsError(t *testing.T) {
	c := New(":invalid", "")
	if _, err := c.Resolve("fixture"); err == nil {
		t.Fatal("invalid resolve URL accepted")
	}
	if _, err := c.Publish(Release{}); err == nil {
		t.Fatal("invalid publish URL accepted")
	}
}
