#!/usr/bin/env bash
# run-infrastructure-mcp.sh - Zero-argument standalone wrapper for Infrastructure MCP Server
# Designed for maximum compatibility with all domestic and overseas MCP clients (Trae, Cursor, Cherry Studio, Chatbox, etc.)
set -euo pipefail

export PATH="/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:${PATH:-}"

INFRA_BIN="/Users/shenlan/.local/bin/infrastructure-mcp-server"
CONFIG_FILE="/Users/shenlan/.config/infrastructure-mcp-server/config.json"

exec "${INFRA_BIN}" -config "${CONFIG_FILE}" "$@"
