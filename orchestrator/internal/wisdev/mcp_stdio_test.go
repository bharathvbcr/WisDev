package wisdev

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestMCPServerStdioInitializeAndToolsList(t *testing.T) {
	srv := NewMCPServer(nil)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	}, "\n") + "\n"

	var output bytes.Buffer
	if err := srv.RunStdio(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatalf("RunStdio: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 responses, got %d: %q", len(lines), output.String())
	}

	var initResp mcpResponse
	if err := json.Unmarshal([]byte(lines[0]), &initResp); err != nil {
		t.Fatalf("decode initialize: %v", err)
	}
	if initResp.Error != nil {
		t.Fatalf("initialize error: %v", initResp.Error)
	}

	var listResp mcpResponse
	if err := json.Unmarshal([]byte(lines[1]), &listResp); err != nil {
		t.Fatalf("decode tools/list: %v", err)
	}
	if listResp.Error != nil {
		t.Fatalf("tools/list error: %v", listResp.Error)
	}
}

func TestMCPServerHandleStdioLineIgnoresNotifications(t *testing.T) {
	srv := NewMCPServer(nil)
	resp, write, err := srv.HandleStdioLine(context.Background(), []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	if err != nil {
		t.Fatalf("HandleStdioLine: %v", err)
	}
	if write {
		t.Fatalf("expected no response for notification, got %q", string(resp))
	}
}

func TestMCPServerHandleStdioLineResourcesList(t *testing.T) {
	srv := NewMCPServer(nil)
	resp, write, err := srv.HandleStdioLine(context.Background(), []byte(`{"jsonrpc":"2.0","id":3,"method":"resources/list"}`))
	if err != nil {
		t.Fatalf("HandleStdioLine: %v", err)
	}
	if !write {
		t.Fatal("expected response for resources/list")
	}
	var decoded mcpResponse
	if err := json.Unmarshal(resp, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Error != nil {
		t.Fatalf("unexpected error: %v", decoded.Error)
	}
}
