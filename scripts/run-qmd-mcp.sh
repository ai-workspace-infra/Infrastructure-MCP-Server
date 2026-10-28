#!/usr/bin/env bash
# run-qmd-mcp.sh - Zero-argument standalone wrapper for QMD MCP Server
# Designed for maximum compatibility with all domestic and overseas MCP clients (Trae, Cursor, Cherry Studio, Chatbox, etc.)
set -euo pipefail

export QMD_BACKEND="pg"
export QMD_PG_URL="postgres://postgres:096fdebb5352ca34c01cf26ca0354ce1@127.0.0.1:15432/qmd?sslmode=disable"
export MODEL_CACHE_DIR="/Users/shenlan/workspaces/qmd-model-cache-20261012"
export PATH="/Users/shenlan/.local/devtools/node-v24.20.0-darwin-arm64/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:${PATH:-}"

NODE_BIN="/Users/shenlan/.local/devtools/node-v24.20.0-darwin-arm64/bin/node"
if [ ! -x "${NODE_BIN}" ]; then
    NODE_BIN="$(command -v node || true)"
fi

QMD_SCRIPT="/Users/shenlan/workspaces/ai-workspace-lab/qmd/dist/cli/qmd.js"

exec "${NODE_BIN}" "${QMD_SCRIPT}" mcp "$@"
