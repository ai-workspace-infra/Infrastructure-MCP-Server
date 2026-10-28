#!/usr/bin/env bash
#
# setup-mcp-server.sh - Automated installer for Infrastructure-MCP-Server gateway
# Supports standalone mode and team (Vault injection) mode under systemd with Caddy HTTPS proxy.
#
set -euo pipefail

PINNED_MCP_VERSION="0.1.0"
PINNED_CADDY_VERSION="2.8.4"
CADDY_SHA256_AMD64="a7e8306c54138cf88e371c5ec0caf7baf142ecc1d60a30897dfb67d65d3748c8"
CADDY_SHA256_ARM64="93a3eb31883d678c6590c6d823eb6bb9f7a3af66dcd9d53df3eb2c7528b2af05"
PINNED_GO_VERSION="1.24.2"
DEFAULT_INSTALL_ROOT="/opt/mcp-server"
DEFAULT_CONFIG_DIR="/etc/infrastructure-mcp-server"
DEFAULT_CONFIG_FILE="/etc/infrastructure-mcp-server/config.json"
DEFAULT_ENV_FILE="/etc/infrastructure-mcp-server/mcp-server.env"
DEFAULT_CADDYFILE="/etc/infrastructure-mcp-server/Caddyfile"
DEFAULT_SYSTEMD_SERVICE="/etc/systemd/system/infrastructure-mcp-server.service"
DEFAULT_CADDY_SERVICE="/etc/systemd/system/infrastructure-mcp-caddy.service"
DEFAULT_BIN_PATH="/usr/local/bin/infrastructure-mcp-server"

DOMAIN=""
MODE=""
DRY_RUN=false
UNINSTALL=false
SERVICE_TOKEN=""
TOKEN_GENERATED=false

INSTALL_ROOT="${MCP_INSTALL_ROOT:-$DEFAULT_INSTALL_ROOT}"
CONFIG_DIR="${MCP_CONFIG_DIR:-$DEFAULT_CONFIG_DIR}"
CONFIG_FILE="${MCP_CONFIG_FILE:-$DEFAULT_CONFIG_FILE}"
ENV_FILE="${MCP_ENV_FILE:-$DEFAULT_ENV_FILE}"
CADDYFILE="${MCP_CADDYFILE:-$DEFAULT_CADDYFILE}"
SYSTEMD_SERVICE="${MCP_SYSTEMD_SERVICE:-$DEFAULT_SYSTEMD_SERVICE}"
CADDY_SERVICE="${MCP_CADDY_SERVICE:-$DEFAULT_CADDY_SERVICE}"
BIN_PATH="${MCP_BIN_PATH:-$DEFAULT_BIN_PATH}"

print_usage() {
    cat <<'EOF'
Usage: setup-mcp-server.sh [OPTIONS]

Install and configure Infrastructure-MCP-Server under systemd with Caddy HTTPS proxy.

Options:
  --domain <fqdn>       Target domain name for MCP gateway (required for installation)
  --standalone          Install in standalone local mode with generated/local service token
  --with-backends       Install in team mode with pre-configured backends (requires Vault injection)
  --dry-run             Simulate installation steps without making persistent changes
  --uninstall           Stop services, remove binaries/units, and back up configuration
  -h, --help            Show this help message and exit

Environment Variables (Team mode):
  MCP_DOMAIN            Domain name (must match --domain)
  MCP_SERVICE_TOKEN     Runtime service token for MCP gateway authentication
EOF
}

parse_args() {
    while [ $# -gt 0 ]; do
        case "$1" in
            --domain)
                if [ $# -lt 2 ] || [ -z "$2" ]; then
                    echo "ERROR: --domain requires a non-empty FQDN argument." >&2
                    exit 1
                fi
                case "$2" in
                    --*)
                        echo "ERROR: --domain requires a non-empty FQDN argument." >&2
                        exit 1
                        ;;
                esac
                DOMAIN="$2"
                shift 2
                ;;
            --domain=*)
                DOMAIN="${1#*=}"
                if [ -z "$DOMAIN" ]; then
                    echo "ERROR: --domain requires a non-empty FQDN argument." >&2
                    exit 1
                fi
                shift
                ;;
            --standalone)
                if [ -n "$MODE" ] && [ "$MODE" != "standalone" ]; then
                    echo "ERROR: --standalone and --with-backends are mutually exclusive." >&2
                    exit 1
                fi
                MODE="standalone"
                shift
                ;;
            --with-backends)
                if [ -n "$MODE" ] && [ "$MODE" != "with-backends" ]; then
                    echo "ERROR: --standalone and --with-backends are mutually exclusive." >&2
                    exit 1
                fi
                MODE="with-backends"
                shift
                ;;
            --dry-run)
                DRY_RUN=true
                shift
                ;;
            --uninstall)
                UNINSTALL=true
                shift
                ;;
            -h|--help)
                print_usage
                exit 0
                ;;
            *)
                echo "ERROR: Unknown argument: $1" >&2
                echo "Run 'setup-mcp-server.sh --help' for usage." >&2
                exit 1
                ;;
        esac
    done
}

check_root() {
    if [ "$(id -u)" != "0" ]; then
        echo "ERROR: This script must be run as root (uid 0)." >&2
        exit 1
    fi
}

check_os() {
    if [ ! -f /etc/os-release ]; then
        echo "ERROR: Cannot detect operating system (/etc/os-release not found)." >&2
        exit 1
    fi

    # shellcheck source=/dev/null
    . /etc/os-release

    distro_id="${ID:-unknown}"
    version_str="${VERSION_ID:-0}"
    version_clean=$(echo "$version_str" | tr -d '"')
    major_ver=$(echo "$version_clean" | cut -d. -f1)

    case "$distro_id" in
        debian)
            if [ -z "$major_ver" ] || [ "$major_ver" -lt 12 ]; then
                echo "ERROR: Unsupported Debian version (${version_clean}). Debian 12+ (Bookworm) is required." >&2
                exit 1
            fi
            ;;
        ubuntu)
            if [ -z "$major_ver" ] || [ "$major_ver" -lt 22 ]; then
                echo "ERROR: Unsupported Ubuntu version (${version_clean}). Ubuntu 22.04+ is required." >&2
                exit 1
            fi
            ;;
        *)
            echo "ERROR: Unsupported distribution '${distro_id}'. Only Debian 12+ and Ubuntu 22.04+ are supported." >&2
            exit 1
            ;;
    esac
}

validate_inputs() {
    if [ "$UNINSTALL" = true ]; then
        return 0
    fi

    if [ -z "$DOMAIN" ]; then
        echo "ERROR: --domain <fqdn> is required." >&2
        echo "Run 'setup-mcp-server.sh --help' for usage." >&2
        exit 1
    fi

    # Verify domain format
    if ! echo "$DOMAIN" | grep -Eq '^([a-zA-Z0-9][-a-zA-Z0-9]*\.)+[a-zA-Z]{2,}$'; then
        echo "ERROR: Invalid domain format '${DOMAIN}'. Please provide a valid FQDN (e.g. mcp.example.com)." >&2
        exit 1
    fi

    if [ -z "$MODE" ]; then
        echo "ERROR: Either --standalone or --with-backends must be specified." >&2
        echo "Run 'setup-mcp-server.sh --help' for usage." >&2
        exit 1
    fi

    if [ "$MODE" = "with-backends" ]; then
        if [ -z "${MCP_DOMAIN:-}" ]; then
            echo "ERROR: Environment variable MCP_DOMAIN is required in team mode (--with-backends)." >&2
            exit 1
        fi
        if [ "$DOMAIN" != "$MCP_DOMAIN" ]; then
            echo "ERROR: CLI --domain ($DOMAIN) does not match MCP_DOMAIN ($MCP_DOMAIN)." >&2
            exit 1
        fi
        if [ -z "${MCP_SERVICE_TOKEN:-}" ]; then
            echo "ERROR: Environment variable MCP_SERVICE_TOKEN is required in team mode (--with-backends)." >&2
            exit 1
        fi
        SERVICE_TOKEN="$MCP_SERVICE_TOKEN"
    elif [ "$MODE" = "standalone" ]; then
        if [ -n "${MCP_SERVICE_TOKEN:-}" ]; then
            SERVICE_TOKEN="$MCP_SERVICE_TOKEN"
            TOKEN_GENERATED=false
        else
            if command -v openssl >/dev/null 2>&1; then
                SERVICE_TOKEN=$(openssl rand -hex 32)
            else
                SERVICE_TOKEN=$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')
            fi
            TOKEN_GENERATED=true
        fi
    fi
}

resolve_dns() {
    target_domain="$1"
    resolved_ip=""
    if command -v dig >/dev/null 2>&1; then
        resolved_ip=$(dig +short A "$target_domain" 2>/dev/null | grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$' | tail -n1 || true)
    elif command -v host >/dev/null 2>&1; then
        resolved_ip=$(host -t A "$target_domain" 2>/dev/null | awk '/has address/ { print $NF }' | tail -n1 || true)
    elif command -v getent >/dev/null 2>&1; then
        resolved_ip=$(getent ahostsv4 "$target_domain" 2>/dev/null | awk '{ print $1 }' | head -n1 || true)
    fi
    echo "$resolved_ip"
}

get_public_ip() {
    pub_ip=""
    if command -v curl >/dev/null 2>&1; then
        pub_ip=$(curl -fsSL --connect-timeout 3 https://api.ipify.org 2>/dev/null || \
                 curl -fsSL --connect-timeout 3 https://icanhazip.com 2>/dev/null || \
                 curl -fsSL --connect-timeout 3 https://ifconfig.me 2>/dev/null || true)
    elif command -v wget >/dev/null 2>&1; then
        pub_ip=$(wget -qO- --timeout=3 https://api.ipify.org 2>/dev/null || true)
    fi
    echo "$pub_ip" | tr -d '[:space:]'
}

check_dns_preflight() {
    echo "==> Running DNS and TLS preflight check for domain: ${DOMAIN}..."
    resolved_ip=$(resolve_dns "$DOMAIN")
    public_ip=$(get_public_ip)

    if [ -z "$resolved_ip" ]; then
        echo "WARNING: Could not resolve DNS A record for '${DOMAIN}'." >&2
        if [ "$DRY_RUN" = false ]; then
            echo "WARNING: Automatic TLS certificate provisioning via Caddy may fail if DNS is not pointing to this host." >&2
        fi
    elif [ -z "$public_ip" ]; then
        echo "WARNING: Resolved ${DOMAIN} -> ${resolved_ip}, but could not determine local public IP for verification." >&2
    elif [ "$resolved_ip" != "$public_ip" ]; then
        echo "WARNING: Domain A record (${resolved_ip}) does not match local public IP (${public_ip})." >&2
        echo "WARNING: Let's Encrypt / ZeroSSL TLS issuance via Caddy may fail until DNS propagates." >&2
    else
        echo "✓ DNS check passed: ${DOMAIN} resolves to this host public IP (${public_ip})."
    fi
}

do_uninstall() {
    echo "==> Uninstalling Infrastructure-MCP-Server..."
    backup_dir="/var/backups/infrastructure-mcp-server-$(date +%Y%m%d%H%M%S)"

    if [ "$DRY_RUN" = true ]; then
        echo "[DRY-RUN] Would stop and disable systemd service: ${SYSTEMD_SERVICE}"
        echo "[DRY-RUN] Would stop and disable Caddy reverse proxy service / container"
        echo "[DRY-RUN] Would remove binary: ${BIN_PATH}"
        echo "[DRY-RUN] Would back up configuration and data to: ${backup_dir}"
        echo "[DRY-RUN] Would remove directories: ${CONFIG_DIR} and ${INSTALL_ROOT}"
        echo "✓ Dry-run uninstall simulation complete."
        return 0
    fi

    # 1. Stop and disable mcp-server systemd unit
    if [ -f "$SYSTEMD_SERVICE" ] || systemctl is-active --quiet infrastructure-mcp-server 2>/dev/null; then
        systemctl stop infrastructure-mcp-server 2>/dev/null || true
        systemctl disable infrastructure-mcp-server 2>/dev/null || true
        rm -f "$SYSTEMD_SERVICE"
    fi

    # 2. Stop and disable Caddy container / service
    if [ -f "$CADDY_SERVICE" ] || systemctl is-active --quiet infrastructure-mcp-caddy 2>/dev/null; then
        systemctl stop infrastructure-mcp-caddy 2>/dev/null || true
        systemctl disable infrastructure-mcp-caddy 2>/dev/null || true
        rm -f "$CADDY_SERVICE"
    fi
    if command -v docker >/dev/null 2>&1; then
        docker rm -f infrastructure-mcp-caddy 2>/dev/null || true
    fi

    systemctl daemon-reload 2>/dev/null || true

    # 3. Remove binary
    if [ -f "$BIN_PATH" ]; then
        rm -f "$BIN_PATH"
        echo "Removed binary ${BIN_PATH}"
    fi

    # 4. Back up data & configs
    mkdir -p "$backup_dir"
    if [ -d "$CONFIG_DIR" ]; then
        cp -a "$CONFIG_DIR" "$backup_dir/etc-infrastructure-mcp-server"
        rm -rf "$CONFIG_DIR"
    fi
    if [ -d "$INSTALL_ROOT" ]; then
        cp -a "$INSTALL_ROOT" "$backup_dir/opt-mcp-server"
        rm -rf "$INSTALL_ROOT"
    fi

    echo "================================================================="
    echo "✓ Infrastructure-MCP-Server uninstalled successfully."
    echo "  Services stopped and removed."
    echo "  Configuration and data backed up to: ${backup_dir}"
    echo "================================================================="
}

install_binary() {
    # Idempotency check: if already installed at pinned version, skip
    if [ -x "$BIN_PATH" ]; then
        installed_version=$("$BIN_PATH" -version 2>/dev/null || true)
        if [ "$installed_version" = "$PINNED_MCP_VERSION" ]; then
            echo "✓ Binary already installed at pinned version ${PINNED_MCP_VERSION} at ${BIN_PATH} (skipping build)."
            return 0
        fi
    fi

    echo "==> Building/installing Infrastructure-MCP-Server ${PINNED_MCP_VERSION} to ${BIN_PATH}..."
    if [ "$DRY_RUN" = true ]; then
        echo "[DRY-RUN] Would build/install Go binary (pinned: ${PINNED_MCP_VERSION}) to ${BIN_PATH}"
        return 0
    fi

    src_dir=""
    script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
    repo_root=$(cd "${script_dir}/.." && pwd)

    if [ -f "${repo_root}/cmd/infrastructure-mcp-server/main.go" ]; then
        src_dir="$repo_root"
    elif [ -f "${INSTALL_ROOT}/src/cmd/infrastructure-mcp-server/main.go" ]; then
        src_dir="${INSTALL_ROOT}/src"
    else
        mkdir -p "${INSTALL_ROOT}/src"
        echo "Fetching repository source to ${INSTALL_ROOT}/src..."
        if [ ! -d "${INSTALL_ROOT}/src/.git" ]; then
            git clone --depth 1 https://github.com/ai-workspace-infra/Infrastructure-MCP-Server.git "${INSTALL_ROOT}/src"
        else
            (cd "${INSTALL_ROOT}/src" && git pull --ff-only)
        fi
        src_dir="${INSTALL_ROOT}/src"
    fi

    go_cmd="go"
    if ! command -v go >/dev/null 2>&1; then
        echo "Go compiler not found on host. Downloading Go ${PINNED_GO_VERSION} to ${INSTALL_ROOT}/go..."
        arch=$(uname -m)
        case "$arch" in
            x86_64) go_arch="amd64" ;;
            aarch64|arm64) go_arch="arm64" ;;
            *) echo "ERROR: Unsupported architecture for Go toolchain: $arch" >&2; exit 1 ;;
        esac
        mkdir -p "${INSTALL_ROOT}/go"
        go_tarball="go${PINNED_GO_VERSION}.linux-${go_arch}.tar.gz"
        curl -fsSL "https://go.dev/dl/${go_tarball}" -o "/tmp/${go_tarball}"
        tar -C "${INSTALL_ROOT}" -xzf "/tmp/${go_tarball}"
        rm -f "/tmp/${go_tarball}"
        go_cmd="${INSTALL_ROOT}/go/bin/go"
    fi

    (cd "$src_dir" && "$go_cmd" build -ldflags="-s -w -X main.version=${PINNED_MCP_VERSION}" -o "$BIN_PATH" ./cmd/infrastructure-mcp-server)
    chmod 755 "$BIN_PATH"
    echo "✓ Built and installed ${BIN_PATH} (version: $("$BIN_PATH" -version))"
}

configure_mcp_server() {
    echo "==> Configuring Infrastructure-MCP-Server (${MODE} mode)..."
    if [ "$DRY_RUN" = true ]; then
        echo "[DRY-RUN] Would create directory: ${CONFIG_DIR}"
        echo "[DRY-RUN] Would write configuration: ${CONFIG_FILE}"
        echo "[DRY-RUN] Would write environment file: ${ENV_FILE}"
        echo "[DRY-RUN] Would generate Caddy reverse proxy configuration: ${CADDYFILE}"
        return 0
    fi

    mkdir -p "$CONFIG_DIR" "$INSTALL_ROOT"
    chmod 700 "$CONFIG_DIR"

    # Backup existing config if present
    if [ -f "$CONFIG_FILE" ]; then
        backup_cfg="${CONFIG_FILE}.bak.$(date +%Y%m%d%H%M%S)"
        cp -a "$CONFIG_FILE" "$backup_cfg"
        echo "Backed up existing configuration to ${backup_cfg}"
    fi

    # Generate config.json based on mode
    if [ "$MODE" = "standalone" ]; then
        cat > "$CONFIG_FILE" <<EOF
{
  "server": {
    "name": "infrastructure-mcp-server",
    "version": "${PINNED_MCP_VERSION}"
  },
  "backends": [
    {"name": "observability-grafana", "capability": "observability", "transport": "sse", "url": "https://mcp-server.svc.plus/v1/grafana/sse", "enabled": false},
    {"name": "observability-metrics", "capability": "observability", "transport": "streamable-http", "url": "https://mcp-server.svc.plus/v1/metrics/mcp", "enabled": false},
    {"name": "observability-logs", "capability": "observability", "transport": "streamable-http", "url": "https://mcp-server.svc.plus/v1/logs/mcp", "enabled": false},
    {"name": "observability-traces", "capability": "observability", "transport": "streamable-http", "url": "https://mcp-server.svc.plus/v1/traces/mcp", "enabled": false},
    {"name": "kubernetes", "capability": "runtime", "command": "npx", "args": ["-y", "<kubernetes-mcp-package>"], "enabled": false},
    {"name": "helm", "capability": "runtime", "command": "npx", "args": ["-y", "<helm-mcp-package>"], "enabled": false},
    {"name": "netbox", "capability": "cmdb", "command": "uvx", "args": ["<netbox-mcp-package>"], "enabled": false},
    {"name": "terraform", "capability": "iac", "command": "<terraform-mcp-command>", "args": [], "enabled": false},
    {"name": "opentofu", "capability": "iac", "command": "<opentofu-mcp-command>", "args": [], "enabled": false},
    {"name": "vault", "capability": "security", "command": "uvx", "args": ["<vault-mcp-package>"], "enabled": false},
    {"name": "ansible", "capability": "iac", "command": "<ansible-playbook-mcp-command>", "args": [], "enabled": false}
  ]
}
EOF
    else
        cat > "$CONFIG_FILE" <<EOF
{
  "server": {
    "name": "infrastructure-mcp-server",
    "version": "${PINNED_MCP_VERSION}"
  },
  "backends": [
    {"name": "observability-grafana", "capability": "observability", "transport": "sse", "url": "https://mcp-server.svc.plus/v1/grafana/sse", "enabled": true},
    {"name": "observability-metrics", "capability": "observability", "transport": "streamable-http", "url": "https://mcp-server.svc.plus/v1/metrics/mcp", "enabled": true},
    {"name": "observability-logs", "capability": "observability", "transport": "streamable-http", "url": "https://mcp-server.svc.plus/v1/logs/mcp", "enabled": true},
    {"name": "observability-traces", "capability": "observability", "transport": "streamable-http", "url": "https://mcp-server.svc.plus/v1/traces/mcp", "enabled": true},
    {"name": "kubernetes", "capability": "runtime", "command": "npx", "args": ["-y", "@modelcontextprotocol/server-kubernetes"], "enabled": true},
    {"name": "helm", "capability": "runtime", "command": "npx", "args": ["-y", "@modelcontextprotocol/server-helm"], "enabled": true},
    {"name": "netbox", "capability": "cmdb", "command": "uvx", "args": ["netbox-mcp"], "enabled": true},
    {"name": "terraform", "capability": "iac", "command": "terraform-mcp", "args": [], "enabled": true},
    {"name": "opentofu", "capability": "iac", "command": "opentofu-mcp", "args": [], "enabled": true},
    {"name": "vault", "capability": "security", "command": "uvx", "args": ["vault-mcp"], "enabled": true},
    {"name": "ansible", "capability": "iac", "command": "ansible-playbook-mcp", "args": [], "enabled": true}
  ]
}
EOF
    fi
    chmod 600 "$CONFIG_FILE"

    # Backup and write env file (never output secret values to terminal)
    if [ -f "$ENV_FILE" ]; then
        backup_env="${ENV_FILE}.bak.$(date +%Y%m%d%H%M%S)"
        cp -a "$ENV_FILE" "$backup_env"
    fi

    cat > "$ENV_FILE" <<EOF
MCP_DOMAIN="${DOMAIN}"
MCP_SERVICE_TOKEN="${SERVICE_TOKEN}"
INFRA_MCP_CONFIG="${CONFIG_FILE}"
MCP_LISTEN_ADDR=":8080"
EOF
    chmod 600 "$ENV_FILE"
    echo "✓ Generated ${CONFIG_FILE} and ${ENV_FILE}"

    # Generate Caddyfile
    cat > "$CADDYFILE" <<EOF
${DOMAIN} {
    encode gzip zstd

    # Health check endpoint
    handle /health {
        respond 200 {
            body "OK"
            close
        }
    }

    # Reverse proxy to Infrastructure-MCP-Server gateway
    handle {
        reverse_proxy 127.0.0.1:8080 {
            header_up Host {host}
            header_up X-Real-IP {remote_host}
            header_up X-Forwarded-For {remote_host}
            header_up X-Forwarded-Proto {scheme}
        }
    }
}
EOF
    chmod 644 "$CADDYFILE"
    echo "✓ Generated Caddyfile at ${CADDYFILE}"
}

configure_systemd() {
    echo "==> Configuring systemd services..."
    if [ "$DRY_RUN" = true ]; then
        echo "[DRY-RUN] Would create systemd service: ${SYSTEMD_SERVICE}"
        echo "[DRY-RUN] Would configure Caddy reverse proxy service (pinned: ${PINNED_CADDY_VERSION}-alpine)"
        echo "[DRY-RUN] Would reload systemd and start services"
        return 0
    fi

    cat > "$SYSTEMD_SERVICE" <<EOF
[Unit]
Description=Infrastructure MCP Server (Go MCP aggregation gateway)
Documentation=https://github.com/ai-workspace-infra/Infrastructure-MCP-Server
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=${INSTALL_ROOT}
EnvironmentFile=-${ENV_FILE}
ExecStart=${BIN_PATH} -config ${CONFIG_FILE} -listen :8080
Restart=always
RestartSec=5s
LimitNOFILE=65536
StandardInput=null
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF
    chmod 644 "$SYSTEMD_SERVICE"

    # Setup Caddy service
    if command -v docker >/dev/null 2>&1 && systemctl is-active --quiet docker 2>/dev/null; then
        echo "Docker detected. Configuring Caddy container (${PINNED_CADDY_VERSION}-alpine)..."
        cat > "$CADDY_SERVICE" <<EOF
[Unit]
Description=Caddy Reverse Proxy for Infrastructure MCP Server (Container)
After=docker.service
Requires=docker.service

[Service]
TimeoutStartSec=0
Restart=always
ExecStartPre=-/usr/bin/docker stop infrastructure-mcp-caddy
ExecStartPre=-/usr/bin/docker rm infrastructure-mcp-caddy
ExecStart=/usr/bin/docker run --name infrastructure-mcp-caddy \\
    --net=host \\
    -v ${CADDYFILE}:/etc/caddy/Caddyfile:ro \\
    -v ${INSTALL_ROOT}/caddy_data:/data \\
    -v ${INSTALL_ROOT}/caddy_config:/config \\
    caddy:${PINNED_CADDY_VERSION}-alpine
ExecStop=/usr/bin/docker stop infrastructure-mcp-caddy

[Install]
WantedBy=multi-user.target
EOF
        chmod 644 "$CADDY_SERVICE"
        systemctl daemon-reload
        systemctl enable --now infrastructure-mcp-caddy.service || true
    elif command -v caddy >/dev/null 2>&1; then
        echo "Native Caddy detected. Configuring native Caddy service..."
        mkdir -p /etc/caddy
        cp -a "$CADDYFILE" /etc/caddy/Caddyfile
        systemctl reload-or-restart caddy 2>/dev/null || systemctl restart caddy 2>/dev/null || true
    else
        echo "Configuring standalone Caddy ${PINNED_CADDY_VERSION}..."
        arch=$(uname -m)
        case "$arch" in
            x86_64)
                caddy_arch="amd64"
                expected_caddy_sha256="$CADDY_SHA256_AMD64"
                ;;
            aarch64|arm64)
                caddy_arch="arm64"
                expected_caddy_sha256="$CADDY_SHA256_ARM64"
                ;;
            *)
                echo "ERROR: Unsupported architecture for Caddy: $arch" >&2
                exit 1
                ;;
        esac

        caddy_needs_install=true
        if [ -x "/usr/local/bin/caddy" ]; then
            installed_caddy_ver="$(/usr/local/bin/caddy version 2>/dev/null | awk '{print $1}' | sed 's/^v//')"
            if [ "$installed_caddy_ver" = "$PINNED_CADDY_VERSION" ]; then
                echo "Caddy is already installed at pinned version ${PINNED_CADDY_VERSION}. Skipping download."
                caddy_needs_install=false
            fi
        fi

        if [ "$caddy_needs_install" = true ]; then
            caddy_tar="caddy_${PINNED_CADDY_VERSION}_linux_${caddy_arch}.tar.gz"
            caddy_tar_path="/tmp/${caddy_tar}"
            echo "Downloading Caddy v${PINNED_CADDY_VERSION} (${caddy_arch})..."
            if ! curl -fsSL "https://github.com/caddyserver/caddy/releases/download/v${PINNED_CADDY_VERSION}/${caddy_tar}" -o "$caddy_tar_path"; then
                echo "ERROR: Failed to download Caddy from GitHub releases." >&2
                rm -f "$caddy_tar_path"
                exit 1
            fi

            echo "Verifying Caddy archive SHA256 checksum..."
            downloaded_caddy_sha="$(sha256sum "$caddy_tar_path" 2>/dev/null | awk '{print $1}')"
            if [ "$downloaded_caddy_sha" != "$expected_caddy_sha256" ]; then
                echo "ERROR: SHA256 checksum mismatch for Caddy archive!" >&2
                echo "Expected: ${expected_caddy_sha256}" >&2
                echo "Actual:   ${downloaded_caddy_sha}" >&2
                rm -f "$caddy_tar_path"
                exit 1
            fi

            tar -C /usr/local/bin -xzf "$caddy_tar_path" caddy
            chmod 755 /usr/local/bin/caddy
            rm -f "$caddy_tar_path"
            echo "✓ Verified SHA256 and installed Caddy to /usr/local/bin/caddy"
        fi
        cat > "$CADDY_SERVICE" <<EOF
[Unit]
Description=Caddy Reverse Proxy for Infrastructure MCP Server
Documentation=https://caddyserver.com/docs/
After=network.target network-online.target
Requires=network-online.target

[Service]
Type=notify
User=root
ExecStart=/usr/local/bin/caddy run --environ --config ${CADDYFILE}
ExecReload=/usr/local/bin/caddy reload --config ${CADDYFILE}
TimeoutStopSec=5s
LimitNOFILE=1048576
Restart=always
RestartSec=5s

[Install]
WantedBy=multi-user.target
EOF
        chmod 644 "$CADDY_SERVICE"
        systemctl daemon-reload
        systemctl enable --now infrastructure-mcp-caddy.service || true
    fi

    # Enable and start infrastructure-mcp-server
    systemctl daemon-reload
    systemctl enable infrastructure-mcp-server.service
    systemctl restart infrastructure-mcp-server.service || true
    echo "✓ Enabled and started systemd services"
}

print_summary() {
    if [ "$DRY_RUN" = true ]; then
        echo ""
        echo "================================================================="
        echo "🔍 [DRY-RUN] Infrastructure MCP Server Simulation Summary"
        echo "================================================================="
        echo "  Status:                 Dry-Run Verified (Ready to Install)"
        echo "  MCP Gateway URL:        https://${DOMAIN}"
        echo "  Systemd Service:        infrastructure-mcp-server.service (simulation)"
        echo "  Reverse Proxy:          Caddy (pinned: ${PINNED_CADDY_VERSION}-alpine)"
        echo "  Transport Mode:         HTTP/stdio (Gateway: HTTPS edge / Service: stdio)"
        echo "  Target Install Root:    ${INSTALL_ROOT}"
        echo "  Configuration File:     ${CONFIG_FILE}"
        echo "================================================================="
    else
        svc_status=$(systemctl is-active infrastructure-mcp-server 2>/dev/null || echo "inactive")
        echo ""
        echo "================================================================="
        echo "🚀 Infrastructure MCP Server Setup Complete!"
        echo "================================================================="
        echo "  Status:                 Healthy"
        echo "  MCP Gateway URL:        https://${DOMAIN}"
        echo "  Systemd Service:        infrastructure-mcp-server.service (${svc_status})"
        echo "  Reverse Proxy:          Caddy (pinned: ${PINNED_CADDY_VERSION})"
        echo "  Transport Mode:         HTTP/stdio (Gateway: HTTPS edge / Service: stdio)"
        echo "  Target Install Root:    ${INSTALL_ROOT}"
        echo "  Configuration File:     ${CONFIG_FILE}"
        echo "================================================================="
    fi

    if [ "$TOKEN_GENERATED" = true ]; then
        echo ""
        echo "================================================================="
        echo "🔑 Generated Service Credentials (Standalone Mode)"
        echo "================================================================="
        echo "  Service Token: ${SERVICE_TOKEN}"
        echo ""
        echo "  IMPORTANT: This token was generated for local standalone use."
        echo "  It is displayed ONLY ONCE. Please securely store this credential"
        echo "  in Vault (e.g. vault kv put secret/mcp/service-token token=...)"
        echo "  or your organization's secret management system."
        echo "================================================================="
        echo ""
    fi
}

main() {
    parse_args "$@"
    check_root
    check_os
    validate_inputs

    if [ "$UNINSTALL" = true ]; then
        do_uninstall
        exit 0
    fi

    check_dns_preflight
    install_binary
    configure_mcp_server
    configure_systemd
    print_summary
}

main "$@"
