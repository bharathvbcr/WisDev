package wisdev

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const defaultMCPStdioBuffer = 1024 * 1024

// RunStdio serves MCP JSON-RPC 2.0 over newline-delimited stdin/stdout.
// Diagnostic output must use stderr; stdout is reserved for MCP responses.
func (s *MCPServer) RunStdio(ctx context.Context, in io.Reader, out io.Writer) error {
	if in == nil {
		in = bytes.NewReader(nil)
	}
	if out == nil {
		out = io.Discard
	}

	scanner := bufio.NewScanner(in)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, defaultMCPStdioBuffer)

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}

		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		response, shouldWrite, err := s.HandleStdioLine(ctx, line)
		if err != nil {
			return err
		}
		if !shouldWrite {
			continue
		}

		if _, err := out.Write(append(response, '\n')); err != nil {
			return err
		}
	}

	return scanner.Err()
}

// HandleStdioLine parses one MCP stdio message and returns the encoded response.
func (s *MCPServer) HandleStdioLine(ctx context.Context, line []byte) ([]byte, bool, error) {
	var req mcpRequest
	if err := json.Unmarshal(line, &req); err != nil {
		payload, err := json.Marshal(mcpResponse{
			JSONRPC: "2.0",
			Error:   &mcpError{Code: mcpErrParse, Message: "parse error: " + err.Error()},
		})
		return payload, true, err
	}

	if isMCPNotification(req.Method) {
		return nil, false, nil
	}

	if req.JSONRPC != "2.0" {
		payload, err := json.Marshal(mcpResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &mcpError{Code: mcpErrInvalidRequest, Message: "jsonrpc must be 2.0"},
		})
		return payload, true, err
	}

	if strings.TrimSpace(req.Method) == "" {
		payload, err := json.Marshal(mcpResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &mcpError{Code: mcpErrInvalidRequest, Message: "method is required"},
		})
		return payload, true, err
	}

	handleCtx := ctx
	if handleCtx == nil {
		handleCtx = context.Background()
	}
	handleCtx, cancel := context.WithTimeout(handleCtx, s.effectiveTimeout())
	defer cancel()

	result, mcpErr := s.dispatch(handleCtx, req)
	if mcpErr != nil {
		payload, err := json.Marshal(mcpResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   mcpErr,
		})
		return payload, true, err
	}

	payload, err := json.Marshal(mcpResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  result,
	})
	return payload, true, err
}

func isMCPNotification(method string) bool {
	return strings.HasPrefix(strings.TrimSpace(method), "notifications/")
}

// EncodeMCPResponse marshals an MCP response for tests and tooling.
func EncodeMCPResponse(id any, result any, mcpErr *mcpError) ([]byte, error) {
	resp := mcpResponse{JSONRPC: "2.0", ID: id, Result: result, Error: mcpErr}
	encoded, err := json.Marshal(resp)
	if err != nil {
		return nil, fmt.Errorf("encode mcp response: %w", err)
	}
	return encoded, nil
}
