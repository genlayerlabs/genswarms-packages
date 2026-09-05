// Package client talks to a swarmidx notary over HTTP: resolve a ref → digest,
// publish a release (token-authenticated), and fetch + verify the transparency
// log (Ed25519) client-side. Authenticity requires a trusted public key;
// signatures alone do not prove freshness or absence of split views.
package client

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf16"
)

type Client struct {
	Endpoint string
	Token    string
	http     *http.Client
}

func New(endpoint, token string) *Client {
	return &Client{
		Endpoint: strings.TrimRight(endpoint, "/"),
		Token:    token,
		http:     &http.Client{Timeout: 15 * time.Second},
	}
}

// Release is the publish payload (matches swarmidx /v1/publish).
type Release struct {
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	Version     string   `json:"version"`
	Digest      string   `json:"digest"` // a claim; the server computes its own and cross-checks
	Source      string   `json:"source"`
	Dir         string   `json:"dir"`
	Description string   `json:"description,omitempty"`
	Docs        string   `json:"docs,omitempty"`
	Skill       string   `json:"skill,omitempty"`
	Module      string   `json:"module,omitempty"`
	Deps        []string `json:"deps,omitempty"`
}

// LogEntry mirrors a swarmidx transparency-log entry.
type LogEntry struct {
	Seq       int64          `json:"seq"`
	Payload   map[string]any `json:"payload"`
	PrevHash  string         `json:"prev_hash"`
	EntryHash string         `json:"entry_hash"`
	Signature string         `json:"signature"`
}

func (c *Client) Publish(r Release) (map[string]any, error) {
	body, _ := json.Marshal(r)
	req, err := http.NewRequest("POST", c.Endpoint+"/v1/publish", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("invalid notary URL")
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	return c.do(req)
}

func (c *Client) PublicKey() (ed25519.PublicKey, error) {
	out, err := c.getJSON(c.Endpoint + "/v1/publickey")
	if err != nil {
		return nil, err
	}
	hexkey, _ := out["public_key"].(string)
	raw, err := hex.DecodeString(hexkey)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("bad public key from server")
	}
	return ed25519.PublicKey(raw), nil
}

func (c *Client) Log(since int) ([]LogEntry, error) {
	out, err := c.getJSON(fmt.Sprintf("%s/v1/log?since=%d", c.Endpoint, since))
	if err != nil {
		return nil, err
	}
	if _, ok := out["entries"].([]any); !ok {
		return nil, fmt.Errorf("invalid log response: entries must be an array")
	}
	b, _ := json.Marshal(out["entries"])
	var entries []LogEntry
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.UseNumber()
	if err := decoder.Decode(&entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// FullLog fetches from genesis until the server returns an empty page. A valid
// prefix can still be withheld by the server; this is not a freshness proof.
func (c *Client) FullLog() ([]LogEntry, error) {
	var entries []LogEntry
	since, size := 0, 0
	for {
		page, err := c.Log(since)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			return entries, nil
		}
		for _, e := range page {
			if e.Seq <= int64(since) || int64(int(e.Seq)) != e.Seq {
				return nil, fmt.Errorf("invalid log pagination sequence")
			}
			since = int(e.Seq)
		}
		encoded, err := json.Marshal(page)
		if err != nil {
			return nil, fmt.Errorf("invalid log page")
		}
		size += len(encoded)
		if len(entries)+len(page) > 100000 || size > 64<<20 {
			return nil, fmt.Errorf("log exceeds verification limit (100000 entries / 64 MiB)")
		}
		entries = append(entries, page...)
	}
}

// VerifyChain recomputes the hash chain and verifies every Ed25519 signature.
// Returns (ok, index-of-first-bad-or-count). The signed message is the RAW hash
// bytes (the server signs bytes.fromhex(entry_hash)), and canonical() must match
// the server's json.dumps(sort_keys=True, separators=(",",":")).
func VerifyChain(entries []LogEntry, pub ed25519.PublicKey) (bool, int) {
	if len(pub) != ed25519.PublicKeySize {
		return false, 0
	}
	prev := ""
	var lastSeq int64
	for i, e := range entries {
		if e.PrevHash != prev || e.Seq <= lastSeq || e.Payload == nil {
			return false, i
		}
		cb, err := canonical(e.Payload)
		if err != nil {
			return false, i
		}
		h := sha256.New()
		h.Write([]byte(prev))
		h.Write(cb)
		eh := hex.EncodeToString(h.Sum(nil))
		if eh != e.EntryHash {
			return false, i
		}
		raw, err := hex.DecodeString(e.EntryHash)
		if err != nil {
			return false, i
		}
		sig, err := hex.DecodeString(e.Signature)
		if err != nil || !ed25519.Verify(pub, raw, sig) {
			return false, i
		}
		prev = e.EntryHash
		lastSeq = e.Seq
	}
	return true, len(entries)
}

// canonical mirrors json.dumps(payload, sort_keys=True, separators=(",",":")):
// Go marshals map keys sorted and compact; SetEscapeHTML(false) matches Python's
// non-escaping of <, >, &. Python's default ensure_ascii=True additionally
// escapes Unicode (including UTF-16 surrogate pairs for non-BMP characters).
func canonical(payload map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(payload); err != nil {
		return nil, err
	}
	var ascii bytes.Buffer
	for _, r := range string(bytes.TrimRight(buf.Bytes(), "\n")) {
		if r < 127 {
			ascii.WriteByte(byte(r))
		} else if r <= 0xffff {
			fmt.Fprintf(&ascii, `\u%04x`, r)
		} else {
			hi, lo := utf16.EncodeRune(r)
			fmt.Fprintf(&ascii, `\u%04x\u%04x`, hi, lo)
		}
	}
	return ascii.Bytes(), nil
}

func (c *Client) getJSON(u string) (map[string]any, error) {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid notary URL")
	}
	return c.do(req)
}

func (c *Client) do(req *http.Request) (map[string]any, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	const maxResponseBytes = 4 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read notary response")
	}
	if len(data) > maxResponseBytes {
		return nil, fmt.Errorf("notary response exceeds 4 MiB")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Do not reflect opaque server response bodies into CLI logs.
		return nil, fmt.Errorf("notary returned HTTP %d", resp.StatusCode)
	}
	var out map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&out); err != nil || out == nil {
		return nil, fmt.Errorf("invalid JSON object from notary")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return nil, fmt.Errorf("trailing data in notary response")
	}
	return out, nil
}
