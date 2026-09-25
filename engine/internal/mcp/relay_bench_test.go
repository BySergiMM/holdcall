package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
)

// The relay's per-message cost, which every message pays whether or not it is
// a tools/call. Holdcall's first duty is to be invisible, and invisible includes
// not being slow enough to notice on a 512 KiB tool result.
//
//	go test -bench BenchmarkRelay -benchtime 2000x ./internal/mcp/
//
// Small is an ordinary request; large is the 512 KiB payload the relay rig
// uses, which is the size at which framing and copying start to matter.

func smallMessage() []byte {
	b, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "list_repositories", "arguments": map[string]any{"owner": "acme"}},
	})
	return append(b, '\n')
}

func largeMessage(kib int) []byte {
	b, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1,
		"result": map[string]any{"content": []any{map[string]any{
			"type": "text", "text": strings.Repeat("x", kib*1024)}}},
	})
	return append(b, '\n')
}

// benchRead is the framing layer alone: read one message back as the exact
// bytes that arrived, which is the property the whole design rests on.
func benchRead(b *testing.B, msg []byte) {
	b.SetBytes(int64(len(msg)))
	stream := bytes.Repeat(msg, 64)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := NewReader(bytes.NewReader(stream))
		for {
			raw, err := r.ReadRaw()
			if err == io.EOF || len(raw) == 0 {
				break
			}
		}
	}
}

func BenchmarkRelayFramingSmall(b *testing.B) { benchRead(b, smallMessage()) }
func BenchmarkRelayFramingLarge(b *testing.B) { benchRead(b, largeMessage(512)) }

// benchInspect is what Holdcall actually does to a message on the way past:
// classify it, and for a tools/call read the tool name and digest the
// arguments. Nothing is re-serialised, so this is the entire cost of
// understanding a message.
func benchInspect(b *testing.B, msg []byte) {
	b.SetBytes(int64(len(msg)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		env, anomaly := Classify(msg)
		if anomaly != AnomalyNone {
			b.Fatalf("unexpected anomaly %q", anomaly)
		}
		if env.IsToolCall() {
			if _, err := env.Call(); err != nil {
				b.Fatalf("unexpected refusal: %v", err)
			}
		}
	}
}

func BenchmarkRelayInspectSmall(b *testing.B) { benchInspect(b, smallMessage()) }

func BenchmarkRelayInspectLarge(b *testing.B) {
	// A large *tools/call*, not a large result: the digest is taken over the
	// arguments, so this is the case where inspection has real work to do.
	args := map[string]any{"blob": strings.Repeat("x", 512*1024)}
	raw, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "upload", "arguments": args},
	})
	benchInspect(b, append(raw, '\n'))
}

// BenchmarkArgumentsDigest isolates the sha256 over params.arguments, the one
// unavoidably size-proportional thing Holdcall does per call.
func BenchmarkArgumentsDigest(b *testing.B) {
	for _, kib := range []int{1, 64, 512} {
		b.Run(fmt.Sprintf("%dKiB", kib), func(b *testing.B) {
			args := map[string]any{"blob": strings.Repeat("x", kib*1024)}
			raw, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "id": 1, "method": "tools/call",
				"params": map[string]any{"name": "upload", "arguments": args},
			})
			env, _ := Classify(append(raw, '\n'))
			b.SetBytes(int64(kib * 1024))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := env.Call(); err != nil {
					b.Fatalf("unexpected refusal: %v", err)
				}
			}
		})
	}
}
