package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ai-workspace-infra/Infrastructure-MCP-Server/internal/cmdb"
	"github.com/ai-workspace-infra/Infrastructure-MCP-Server/internal/config"
	"github.com/ai-workspace-infra/Infrastructure-MCP-Server/internal/gateway"
	"github.com/ai-workspace-infra/Infrastructure-MCP-Server/internal/graph"
	"github.com/ai-workspace-infra/Infrastructure-MCP-Server/internal/mcp"
	"github.com/ai-workspace-infra/Infrastructure-MCP-Server/internal/sshconfig"
)

var version = "dev"

func main() {
	configPath := flag.String("config", "", "path to a JSON configuration file")
	showVersion := flag.Bool("version", false, "print version and exit")
	listenAddr := flag.String("listen", "", "HTTP listen address (e.g. :8080); if empty, serves on stdio; also reads MCP_LISTEN_ADDR")
	basePath := flag.String("base-path", "", "base path prefix for public reverse proxy (e.g. /v1 or empty); also reads MCP_BASE_PATH")
	renderSSH := flag.String("render-ssh-config", "", "render the ssh-mcp-server config from CMDB host resources to this path and exit")
	sshOut := flag.String("ssh-config-out", "", "startup hook: render the ssh-mcp-server config from CMDB to this path before serving; also reads INFRA_SSH_CONFIG_OUT")
	sshPolicy := flag.String("ssh-policy", "", "optional JSON policy (commandWhitelist, ...) for rendered SSH connections; also reads INFRA_SSH_POLICY_FILE")
	sshHostMap := flag.String("ssh-host-map", "", "optional JSON {hostname: address} (Vault-sourced, never committed) used when rendering SSH connections; also reads INFRA_SSH_HOST_MAP_FILE")
	sshEnv := flag.String("ssh-env", "", "comma-separated environments to include (e.g. sit,prod); default all; also reads INFRA_SSH_ENVIRONMENTS")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	sshOpts := sshRenderOptions{
		policy:  firstNonEmpty(*sshPolicy, os.Getenv("INFRA_SSH_POLICY_FILE")),
		envs:    firstNonEmpty(*sshEnv, os.Getenv("INFRA_SSH_ENVIRONMENTS")),
		hostMap: firstNonEmpty(*sshHostMap, os.Getenv("INFRA_SSH_HOST_MAP_FILE")),
	}
	if *renderSSH != "" {
		store := cmdb.NewStore(graph.NewStore(), "")
		if err := renderSSHConfig(store, *renderSSH, sshOpts); err != nil {
			fmt.Fprintf(os.Stderr, "render ssh config: %v\n", err)
			os.Exit(1)
		}
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
	if out := firstNonEmpty(*sshOut, os.Getenv("INFRA_SSH_CONFIG_OUT")); out != "" {
		// Must run before the ssh backend child process is spawned (backends
		// start lazily on first use). A failure leaves the ssh backend
		// unavailable but does not stop the gateway.
		if err := renderSSHConfig(cmdbStore, out, sshOpts); err != nil {
			fmt.Fprintf(os.Stderr, "WARNING: ssh config startup render failed, ssh backend will be unavailable: %v\n", err)
		}
	}
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
		if token := os.Getenv("MCP_SERVICE_TOKEN"); token != "" {
			httpServer.SetAuthToken(token)
			fmt.Fprintln(os.Stderr, "infrastructure-mcp-server: inbound Bearer auth enabled (MCP_SERVICE_TOKEN)")
		} else {
			fmt.Fprintln(os.Stderr, "WARNING: MCP_SERVICE_TOKEN is not set: the HTTP listener accepts UNAUTHENTICATED requests on /mcp, /sse and /api/v1/*. Set MCP_SERVICE_TOKEN (or front it with an authenticating proxy) before exposing it publicly.")
		}
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

type sshRenderOptions struct {
	policy  string
	envs    string
	hostMap string
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// renderSSHConfig turns CMDB host resources into an ssh-mcp-server config.
// The credential comes from SSH_MCP_PRIVATE_KEY_PATH (a key file the
// operator materialised from the Vault path in each host's vault_ref) or
// SSH_AUTH_SOCK; no secret is read from the CMDB or git.
func renderSSHConfig(store *cmdb.Store, out string, opts sshRenderOptions) error {
	policy, err := sshconfig.LoadPolicy(opts.policy)
	if err != nil {
		return err
	}
	hostMap, err := sshconfig.LoadHostMap(opts.hostMap)
	if err != nil {
		return err
	}
	var envs []string
	if opts.envs != "" {
		envs = strings.Split(opts.envs, ",")
	}
	conns, refs, err := sshconfig.Render(store.SSHTargets(envs...), policy, sshconfig.Options{
		PrivateKeyPath: os.Getenv("SSH_MCP_PRIVATE_KEY_PATH"),
		AgentSocket:    os.Getenv("SSH_AUTH_SOCK"),
		HostMap:        hostMap,
	})
	if err != nil {
		return err
	}
	if err := sshconfig.WriteFile(out, conns); err != nil {
		return err
	}
	names := make([]string, 0, len(conns))
	for _, c := range conns {
		names = append(names, c.Username+"@"+c.Name)
	}
	fmt.Fprintf(os.Stderr, "infrastructure-mcp-server: rendered %d SSH connections from CMDB to %s (%s); credentials expected from Vault refs: %s\n",
		len(conns), out, strings.Join(names, ", "), strings.Join(refs, ", "))
	return nil
}
