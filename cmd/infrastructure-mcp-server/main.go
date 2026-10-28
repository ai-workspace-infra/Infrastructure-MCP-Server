package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ai-workspace-infra/Infrastructure-MCP-Server/internal/cmdb"
	"github.com/ai-workspace-infra/Infrastructure-MCP-Server/internal/config"
	"github.com/ai-workspace-infra/Infrastructure-MCP-Server/internal/gateway"
	"github.com/ai-workspace-infra/Infrastructure-MCP-Server/internal/graph"
	"github.com/ai-workspace-infra/Infrastructure-MCP-Server/internal/mcp"
)

var version = "dev"

func main() {
	configPath := flag.String("config", "", "path to a JSON configuration file")
	showVersion := flag.Bool("version", false, "print version and exit")
	listenAddr := flag.String("listen", "", "HTTP listen address (e.g. :8080); if empty, serves on stdio; also reads MCP_LISTEN_ADDR")
	basePath := flag.String("base-path", "", "base path prefix for public reverse proxy (e.g. /v1 or empty); also reads MCP_BASE_PATH")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	graphStore := graph.NewStore()
	cmdbStore := cmdb.NewStore(graphStore, "")
	gw := gateway.New(cfg, graphStore, cmdbStore)
	server := mcp.NewServer(gw)

	addr := *listenAddr
	if addr == "" {
		addr = os.Getenv("MCP_LISTEN_ADDR")
	}

	if addr != "" {
		bp := *basePath
		if bp == "" {
			bp = os.Getenv("MCP_BASE_PATH")
		}
		httpServer := mcp.NewHTTPServer(server, bp)
		srv := &http.Server{
			Addr:         addr,
			Handler:      httpServer.Handler(),
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 60 * time.Second,
			IdleTimeout:  120 * time.Second,
		}

		go func() {
			<-ctx.Done()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdownCtx)
		}()

		fmt.Fprintf(os.Stderr, "infrastructure-mcp-server listening on %s (base-path: %q)\n", addr, bp)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(os.Stderr, "listen and serve: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if err := server.Serve(ctx, os.Stdin, os.Stdout); err != nil && ctx.Err() == nil {
		fmt.Fprintf(os.Stderr, "serve MCP: %v\n", err)
		os.Exit(1)
	}
}
