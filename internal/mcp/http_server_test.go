package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type dummyProvider struct{}

func (d *dummyProvider) Tools(ctx context.Context) ([]Tool, error) {
	return []Tool{
		{Name: "dummy.tool", Description: "Dummy description", InputSchema: map[string]any{"type": "object"}},
	}, nil
}

func (d *dummyProvider) Call(ctx context.Context, name string, args map[string]any) (CallToolResult, error) {
	return CallToolResult{
		Content: []ContentBlock{{Type: "text", Text: "dummy result"}},
	}, nil
}

func TestHTTPServerHealthz(t *testing.T) {
	srv := NewServer(&dummyProvider{})
	httpSrv := NewHTTPServer(srv, "")
	ts := httptest.NewServer(httpSrv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("healthz request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}
}

func TestHTTPServerStreamableMCP(t *testing.T) {
	srv := NewServer(&dummyProvider{})
	httpSrv := NewHTTPServer(srv, "")
	ts := httptest.NewServer(httpSrv.Handler())
	defer ts.Close()

	reqBody := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	resp, err := http.Post(ts.URL+"/mcp", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("mcp request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	var rpcResp Response
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if rpcResp.Error != nil {
		t.Fatalf("unexpected rpc error: %v", rpcResp.Error)
	}
}

func TestHTTPServerSSEHandshakeAndMessage(t *testing.T) {
	srv := NewServer(&dummyProvider{})
	httpSrv := NewHTTPServer(srv, "")
	ts := httptest.NewServer(httpSrv.Handler())
	defer ts.Close()

	client := ts.Client()
	req, _ := http.NewRequest("GET", ts.URL+"/sse", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /sse failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	reader := bufio.NewReader(resp.Body)
	// Read initial endpoint event
	var endpointLine string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("reading sse failed: %v", err)
		}
		if strings.HasPrefix(line, "data: ") {
			endpointLine = strings.TrimSpace(strings.TrimPrefix(line, "data: "))
			break
		}
	}

	if !strings.Contains(endpointLine, "/message?sessionid=") {
		t.Fatalf("unexpected endpoint line: %s", endpointLine)
	}

	// Send POST message with ping
	postURL := ts.URL + endpointLine
	pingReq := `{"jsonrpc":"2.0","id":2,"method":"ping"}`
	postResp, err := http.Post(postURL, "application/json", bytes.NewBufferString(pingReq))
	if err != nil {
		t.Fatalf("POST message failed: %v", err)
	}
	defer postResp.Body.Close()
	if postResp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d", postResp.StatusCode)
	}

	// Read response over SSE
	done := make(chan bool)
	go func() {
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(line, "data: ") {
				var received Response
				data := strings.TrimPrefix(line, "data: ")
				if err := json.Unmarshal([]byte(data), &received); err == nil && string(received.ID) == "2" {
					done <- true
					return
				}
			}
		}
	}()

	select {
	case <-done:
		// success
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for ping response on SSE")
	}
}
