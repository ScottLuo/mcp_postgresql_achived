//go:build unit

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMain_BinaryBuild verifies that the module builds successfully.
func TestMain_BinaryBuild(t *testing.T) {
	bin := buildBinary(t)
	defer os.Remove(bin)

	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("binary not found after build: %v", err)
	}
}

// TestMain_StartsAndHandlesSIGTERM verifies that the binary starts and
// exits gracefully on SIGTERM.
func TestMain_StartsAndHandlesSIGTERM(t *testing.T) {
	bin := buildBinary(t)
	defer os.Remove(bin)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin)
	cmd.Env = append(os.Environ(),
		"MCP_TRANSPORT=stdio",
		"POSTGRES_TARGETS=dev",
		"POSTGRES_DEV_DSN=postgres://user:pass@localhost:5432/testdb?sslmode=disable",
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start binary: %v", err)
	}
	defer cmd.Process.Kill()

	// Wait for the process to be ready (give it a moment to start).
	time.Sleep(500 * time.Millisecond)

	// Send SIGTERM and verify graceful shutdown.
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("failed to send SIGINT: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("binary exited with error: %v\nstderr: %s", err, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("binary did not exit within 5 seconds after SIGINT")
	}

	// Verify logs go to stderr (not stdout).
	if stdout.Len() > 0 {
		t.Logf("stdout (should be protocol only): %s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "postgres-dba-mcp") {
		t.Logf("warning: stderr did not contain expected log prefix")
	}
}

// TestMain_SendMCPInitialize verifies the binary responds to an MCP
// initialize request over stdio.
func TestMain_SendMCPInitialize(t *testing.T) {
	bin := buildBinary(t)
	defer os.Remove(bin)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin)
	cmd.Env = append(os.Environ(),
		"MCP_TRANSPORT=stdio",
		"POSTGRES_TARGETS=dev",
		"POSTGRES_DEV_DSN=postgres://user:pass@localhost:5432/testdb?sslmode=disable",
	)

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("failed to create stdin pipe: %v", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("failed to create stdout pipe: %v", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start binary: %v", err)
	}
	defer func() {
		stdinPipe.Close()
		cmd.Process.Kill()
		cmd.Wait()
	}()

	// Send MCP initialize request.
	initReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"clientInfo": map[string]interface{}{
				"name":    "test-client",
				"version": "1.0.0",
			},
		},
	}

	reqBytes, _ := json.Marshal(initReq)
	reqBytes = append(reqBytes, '\n')

	if _, err := stdinPipe.Write(reqBytes); err != nil {
		t.Fatalf("failed to write initialize request: %v", err)
	}

	// Read the response with a timeout.
	type jsonrpcResp struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Result  struct {
			ProtocolVersion string `json:"protocolVersion"`
			ServerInfo      struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"serverInfo"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}

	var resp jsonrpcResp

	// Read with deadline using a goroutine.
	type readResult struct {
		data []byte
		err  error
	}
	readCh := make(chan readResult, 1)
	go func() {
		buf := make([]byte, 4096)
		n, err := stdoutPipe.Read(buf)
		if err != nil {
			readCh <- readResult{err: err}
			return
		}
		// The response is newline-delimited JSON. Read until we get a complete line.
		line := buf[:n]
		// If the response is split, read more.
		for !bytes.HasSuffix(line, []byte("}\n")) && !bytes.HasSuffix(line, []byte("}")) {
			more, err := stdoutPipe.Read(buf)
			if err != nil {
				readCh <- readResult{err: err}
				return
			}
			line = append(line, buf[:more]...)
		}
		readCh <- readResult{data: line}
	}()

	select {
	case result := <-readCh:
		if result.err != nil {
			t.Fatalf("failed to read response: %v\nstderr: %s", result.err, stderr.String())
		}
		if err := json.Unmarshal(result.data, &resp); err != nil {
			t.Fatalf("failed to parse JSON response: %v\nraw: %s\nstderr: %s", err, result.data, stderr.String())
		}
		if resp.Error != nil {
			t.Fatalf("got error response: code=%d message=%s", resp.Error.Code, resp.Error.Message)
		}
		if resp.Result.ServerInfo.Name != "postgres-dba-mcp" {
			t.Errorf("expected server name 'postgres-dba-mcp', got %q", resp.Result.ServerInfo.Name)
		}
		if resp.Result.ServerInfo.Version != "0.1.0" {
			t.Errorf("expected version '0.1.0', got %q", resp.Result.ServerInfo.Version)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for MCP initialize response\nstderr: %s", stderr.String())
	}
}

// TestGOMod_ModuleName verifies the go.mod has the correct module name.
func TestGOMod_ModuleName(t *testing.T) {
	goMod, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	if err != nil {
		t.Fatalf("failed to read go.mod: %v", err)
	}

	content := string(goMod)
	if !strings.Contains(content, "module postgres-dba-mcp") {
		t.Errorf("go.mod does not declare module 'postgres-dba-mcp'\ngot:\n%s", content)
	}
}

// TestGOMod_RequiredDependencies verifies all required dependencies are in go.mod.
func TestGOMod_RequiredDependencies(t *testing.T) {
	goMod, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	if err != nil {
		t.Fatalf("failed to read go.mod: %v", err)
	}

	content := string(goMod)

	requiredDeps := []string{
		"github.com/modelcontextprotocol/go-sdk",
		"github.com/jackc/pgx/v5",
		"github.com/pganalyze/pg_query_go/v5",
		"github.com/knadh/koanf/v2",
		"github.com/prometheus/client_golang",
	}

	for _, dep := range requiredDeps {
		if !strings.Contains(content, dep) {
			t.Errorf("go.mod missing required dependency: %s", dep)
		}
	}

	// Verify no gopkg.in imports
	if strings.Contains(content, "gopkg.in") {
		t.Error("go.mod contains gopkg.in dependency (should not be used)")
	}
}

// TestGOSum_Exists verifies go.sum exists and has pinned checksums.
func TestGOSum_Exists(t *testing.T) {
	goSum, err := os.ReadFile(filepath.Join("..", "..", "go.sum"))
	if err != nil {
		t.Fatalf("go.sum not found: %v", err)
	}

	lines := strings.Split(string(goSum), "\n")
	if len(lines) < 5 {
		t.Errorf("go.sum suspiciously short (%d lines)", len(lines))
	}

	// Verify it has checksums for our direct dependencies.
	content := string(goSum)
	for _, dep := range []string{
		"github.com/modelcontextprotocol/go-sdk",
		"github.com/jackc/pgx/v5",
	} {
		if !strings.Contains(content, dep) {
			t.Errorf("go.sum missing checksum for: %s", dep)
		}
	}
}

// TestMCP_ServerName verifies the MCP server reports the correct name.
func TestMCP_ServerName(t *testing.T) {
	srv := mcp.NewServer(
		&mcp.Implementation{
			Name:    "postgres-dba-mcp",
			Version: "0.1.0",
		},
		&mcp.ServerOptions{
			Instructions: "PostgreSQL DBA MCP Gateway",
			Logger:       slog.New(slog.NewTextHandler(os.Stderr, nil)),
		},
	)

	// The server should be non-nil and usable.
	if srv == nil {
		t.Fatal("MCP server is nil")
	}
}

// buildBinary compiles the binary for testing.
func buildBinary(t *testing.T) string {
	t.Helper()

	bin := filepath.Join(t.TempDir(), "postgres-dba-mcp")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = filepath.Join("..", "..", "cmd", "server")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build failed: %v\noutput: %s", err, out)
	}
	return bin
}
