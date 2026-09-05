package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/genlayerlabs/genswarms-packages/cli/internal/client"
)

func TestLogVerifiesGenesisWithSinceAndPinnedKey(t *testing.T) {
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	var entries []client.LogEntry
	prev := ""
	for i := 1; i <= 2; i++ {
		payload := map[string]any{"ref": "swarmidx:fixture/body@" + strconv.Itoa(i)}
		data, _ := json.Marshal(payload)
		hash := sha256.Sum256(append([]byte(prev), data...))
		entry := client.LogEntry{Seq: int64(i), Payload: payload, PrevHash: prev,
			EntryHash: hex.EncodeToString(hash[:]), Signature: hex.EncodeToString(ed25519.Sign(key, hash[:]))}
		entries = append(entries, entry)
		prev = entry.EntryHash
	}
	var pages []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/log" {
			t.Error("pinned-key verification must not fetch a key from the server")
			http.Error(w, "unexpected request", 500)
			return
		}
		since := r.URL.Query().Get("since")
		pages = append(pages, since)
		page := []client.LogEntry{}
		if since == "0" {
			page = entries[:1]
		} else if since == "1" {
			page = entries[1:]
		}
		json.NewEncoder(w).Encode(map[string]any{"entries": page})
	}))
	defer server.Close()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	original := os.Stdout
	os.Stdout = writer
	defer func() { os.Stdout = original; writer.Close() }()
	err = cmdLog([]string{"--endpoint", server.URL, "--since", "1", "--public-key", hex.EncodeToString(key.Public().(ed25519.PublicKey))})
	writer.Close()
	os.Stdout = original
	output, readErr := io.ReadAll(reader)
	if err != nil || readErr != nil {
		t.Fatalf("log failed: %v, read: %v", err, readErr)
	}
	if strings.Join(pages, ",") != "0,1,2" || strings.Contains(string(output), "#1 ") || !strings.Contains(string(output), "#2 ") || !strings.Contains(string(output), "2 returned entries") {
		t.Fatalf("wrong verification/display: pages=%v output=%s", pages, output)
	}
}

func TestLogRejectsInvalidArgumentsBeforeNetwork(t *testing.T) {
	for _, args := range [][]string{{"--since", "-1"}, {"--public-key", "invalid"}, {"unexpected"}} {
		if err := cmdLog(append([]string{"--endpoint", "http://127.0.0.1:1"}, args...)); err == nil || strings.Contains(err.Error(), "connect") {
			t.Fatalf("invalid arguments reached network: %v: %v", args, err)
		}
	}
}
