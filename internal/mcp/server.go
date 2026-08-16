package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
)

type ToolProvider interface {
	Tools(context.Context) ([]Tool, error)
	Call(context.Context, string, map[string]any) (CallToolResult, error)
}

type Server struct {
	provider ToolProvider
}

func NewServer(provider ToolProvider) *Server {
	return &Server{provider: provider}
}

// Serve implements the MCP stdio transport: one JSON-RPC message per line.
func (s *Server) Serve(ctx context.Context, input io.Reader, output io.Writer) error {
	reader := bufio.NewReader(input)
	writer := bufio.NewWriter(output)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			var request Request
			if decodeErr := json.Unmarshal(line, &request); decodeErr != nil {
				if writeErr := writeResponse(writer, Response{JSONRPC: "2.0", Error: &ResponseError{Code: -32700, Message: decodeErr.Error()}}); writeErr != nil {
					return writeErr
				}
			} else if request.Method != "" {
				if handleErr := s.handle(ctx, writer, request); handleErr != nil {
					return handleErr
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
}

func (s *Server) handle(ctx context.Context, writer *bufio.Writer, request Request) error {
	if len(request.ID) == 0 && request.Method == "notifications/initialized" {
		return nil
	}
	if len(request.ID) == 0 {
		return nil
	}

	response := Response{JSONRPC: "2.0", ID: request.ID}
	switch request.Method {
	case "initialize":
		response.Result = map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]string{"name": "infrastructure-mcp-server", "version": "0.1.0"},
		}
	case "ping":
		response.Result = map[string]any{}
	case "tools/list":
		tools, err := s.provider.Tools(ctx)
		if err != nil {
			response.Error = &ResponseError{Code: -32603, Message: err.Error()}
		} else {
			response.Result = map[string]any{"tools": tools}
		}
	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil || params.Name == "" {
			response.Error = &ResponseError{Code: -32602, Message: "tools/call requires a tool name and valid arguments"}
			break
		}
		result, err := s.provider.Call(ctx, params.Name, params.Arguments)
		if err != nil {
			response.Error = &ResponseError{Code: -32603, Message: err.Error()}
		} else {
			response.Result = result
		}
	default:
		response.Error = &ResponseError{Code: -32601, Message: fmt.Sprintf("method %q not found", request.Method)}
	}
	return writeResponse(writer, response)
}

func writeResponse(writer *bufio.Writer, response Response) error {
	data, err := json.Marshal(response)
	if err != nil {
		return err
	}
	if _, err := writer.Write(append(data, '\n')); err != nil {
		return err
	}
	return writer.Flush()
}
