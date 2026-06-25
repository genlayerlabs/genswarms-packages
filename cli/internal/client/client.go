// Package client talks to a swarmidx notary over HTTP: resolve a ref → digest,
// publish a release (token-authenticated), and fetch + verify the transparency
// log (Ed25519) client-side — so the CLI trusts the math, not the server.
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
	"net/url"
	"strings"
	"time"
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
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
	Source  string `json:"source"`
}

// LogEntry mirrors a swarmidx transparency-log entry.
type LogEntry struct {
	Seq       int64          `json:"seq"`
	Payload   map[string]any `json:"payload"`
	PrevHash  string         `json:"prev_hash"`
	EntryHash string         `json:"entry_hash"`
	Signature string         `json:"signature"`
}

func (c *Client) Resolve(ref string) (map[string]any, error) {
	return c.getJSON(c.Endpoint + "/v1/resolve?ref=" + url.QueryEscape(ref))
}

func (c *Client) Publish(r Release) (map[string]any, error) {
	body, _ := json.Marshal(r)
	req, _ := http.NewRequest("POST", c.Endpoint+"/v1/publish", bytes.NewReader(body))
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
	b, _ := json.Marshal(out["entries"])
	var entries []LogEntry
	if err := json.Unmarshal(b, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// VerifyChain recomputes the hash chain and verifies every Ed25519 signature.
// Returns (ok, index-of-first-bad-or-count). The signed message is the RAW hash
// bytes (the server signs bytes.fromhex(entry_hash)), and canonical() must match
// the server's json.dumps(sort_keys=True, separators=(",",":")).
func VerifyChain(entries []LogEntry, pub ed25519.PublicKey) (bool, int) {
	prev := ""
	for i, e := range entries {
		if e.PrevHash != prev {
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
	}
	return true, len(entries)
}

// canonical mirrors json.dumps(payload, sort_keys=True, separators=(",",":")):
// Go marshals map keys sorted and compact; SetEscapeHTML(false) matches Python's
// non-escaping of <, >, &.
func canonical(payload map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(payload); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func (c *Client) getJSON(u string) (map[string]any, error) {
	req, _ := http.NewRequest("GET", u, nil)
	return c.do(req)
}

func (c *Client) do(req *http.Request) (map[string]any, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if len(data) > 0 {
		_ = json.Unmarshal(data, &out)
	}
	if resp.StatusCode >= 400 {
		msg, _ := out["error"].(string)
		if msg == "" {
			msg = strings.TrimSpace(string(data))
		}
		return out, fmt.Errorf("%s: %s", resp.Status, msg)
	}
	return out, nil
}
