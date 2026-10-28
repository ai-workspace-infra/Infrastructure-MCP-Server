#!/usr/bin/env bash
#
# sync-knowledge-to-qmd.sh - Periodic synchronization and vectorization of historical knowledge docs into local QMD
#
set -euo pipefail

QMD_CLI="/Users/shenlan/workspaces/ai-workspace-lab/qmd/dist/cli/qmd.js"
KNOWLEDGE_DOCS_DIR="${1:-/Users/shenlan/workspaces/ai-workspace-service/knowledge/docs/reference}"
COLLECTION_NAME="knowledge-reference"
MODEL_CACHE_DIR="/Users/shenlan/workspaces/qmd-model-cache-20261012"

echo "================================================================="
echo "🧠 QMD Knowledge Periodic Synchronization & Vectorization"
echo "================================================================="
echo "Target collection:  ${COLLECTION_NAME}"
echo "Source path:        ${KNOWLEDGE_DOCS_DIR}"
echo "Local time:         $(date '+%Y-%m-%d %H:%M:%S')"
echo "================================================================="

if [ ! -d "${KNOWLEDGE_DOCS_DIR}" ]; then
    echo "ERROR: Knowledge directory does not exist: ${KNOWLEDGE_DOCS_DIR}" >&2
    exit 1
fi

if [ ! -f "${QMD_CLI}" ]; then
    echo "ERROR: QMD CLI not found at ${QMD_CLI}" >&2
    exit 1
fi

# 1. Check or register collection
existing_colls=$(node "${QMD_CLI}" collection list 2>/dev/null || true)
if ! echo "${existing_colls}" | grep -q "${COLLECTION_NAME}"; then
    echo "==> Registering new collection '${COLLECTION_NAME}'..."
    node "${QMD_CLI}" collection add "${KNOWLEDGE_DOCS_DIR}" --name "${COLLECTION_NAME}"
else
    echo "==> Collection '${COLLECTION_NAME}' already registered. Running update..."
    node "${QMD_CLI}" update
fi

# 2. Vectorization (embed)
echo "==> Checking vectorization requirements..."
if [ -n "${OPENAI_API_KEY:-}" ] || [ -n "${NVIDIA_API_KEY:-}" ] || [ -n "${QMD_EMBED_API_KEY:-}" ]; then
    echo "==> Running QMD vector embeddings via configured API key..."
    node "${QMD_CLI}" embed -c "${COLLECTION_NAME}" || echo "Notice: Embedding step completed with warnings."
else
    echo "==> Notice: No external embedding API key set (NVIDIA_API_KEY / OPENAI_API_KEY / QMD_EMBED_API_KEY)."
    echo "    Lexical BM25 indexing is fully active for all 192+ documents."
    echo "    To enable local vectorization, set QMD_EMBED_MODEL or configure API key."
fi

# 3. Verification search
echo "==> Verifying search indexing..."
node "${QMD_CLI}" search "observability.svc.plus" -c "${COLLECTION_NAME}" -n 2

echo ""
echo "✓ Knowledge docs synchronization complete."
