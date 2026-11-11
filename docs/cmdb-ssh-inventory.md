# CMDB-driven SSH inspection inventory

The SSH inspection targets used by `classfang/ssh-mcp-server` are **not**
maintained by hand. They are CMDB resources of `kind: host`, and the
ssh-mcp-server `--config-file` is rendered from them.

```
IaC / GitOps declarations ──► CMDB (kind=host, ssh block) ──► render ──► ssh-config.json (0600)
                                                                  ▲
                         Vault KV (vault_ref) ──► key file / agent ┘   (operator / init step)
```

## CMDB model

Every host resource carries a non-secret `ssh` block:

| field | meaning |
|---|---|
| `ssh.host` | hostname or `~/.ssh/config` alias — never an IP in git |
| `ssh.user` | read-only account, default `auditor` |
| `ssh.port` | default 22 |
| `ssh.connection_name` | ssh-mcp-server `connectionName` (first DNS label, matches the VictoriaMetrics `instance` prefix) |
| `ssh.vault_ref` | Vault KV v2 path holding the private key (and, if the hostname is an alias, the address). Default (proposed): `kv/data/CICD/infrastructure-mcp` |

Labels: `environment` (sit/prod), `provider`, `attributes.role`
(`k3s-node`, `edge-agent`), `attributes.cluster`.

Query them through the gateway:

```json
{"name": "infra.cmdb.query", "arguments": {"kind": "host", "environment": "prod"}}
```

## Inventory sources

1. Built-in seed (`internal/cmdb/hosts.go`): SIT/PROD K3s nodes and the 8 edge nodes, hostnames only.
2. Inventory file, merged by hostname (override / add; `"inspect": false` removes SSH access):
   `$INFRA_CMDB_HOSTS_FILE`, else `<CMDB base dir>/cmdb/hosts.json`.
   The base dir comes from `$INFRA_CMDB_BASE_DIR` (previously hard-coded).

```json
[
  {"hostname": "sg-xconnect.svc.plus", "environment": "prod", "role": "edge-agent", "provider": "vultr"},
  {"hostname": "new-edge.svc.plus", "environment": "prod", "role": "edge-agent", "provider": "aws",
   "ssh_user": "auditor", "vault_ref": "kv/data/CICD/infrastructure-mcp"}
]
```

## Rendering

One-shot:

```bash
# key materialised from Vault into a tmpfs file, never committed
vault kv get -field=ssh_private_key kv/CICD/infrastructure-mcp > /run/ssh-mcp/auditor_ed25519 && chmod 600 /run/ssh-mcp/auditor_ed25519
SSH_MCP_PRIVATE_KEY_PATH=/run/ssh-mcp/auditor_ed25519 \
  infrastructure-mcp-server -render-ssh-config /run/ssh-mcp/ssh-config.json -ssh-env sit,prod
```

Startup hook (renders before the lazily spawned `ssh` backend starts):
`-ssh-config-out /run/ssh-mcp/ssh-config.json` or `INFRA_SSH_CONFIG_OUT`.
A render failure only disables the ssh backend.

Options: `-ssh-env` / `INFRA_SSH_ENVIRONMENTS`, `-ssh-policy` /
`INFRA_SSH_POLICY_FILE` (JSON overriding the built-in read-only policy:
`commandWhitelist`, `commandBlacklist`, `allowedRemotePaths`, timeouts). The
renderer refuses to write a config without a credential (key path or
`SSH_AUTH_SOCK`) or without a command whitelist, and writes the file `0600`.

Pair it with the gateway tool policy so the file-transfer tools are never exposed:

```json
{"name": "ssh", "allowed_tools": ["list-servers", "execute-command"],
 "disabled_tools": ["write-file", "upload", "download", "read-file", "job-*"]}
```

## Follow-ups (not implemented)

- Hot reload: re-render on `infra.cmdb.sync` and restart the ssh child process.
- Direct Vault fetch in the renderer (today the operator/init step materialises the key).
- Real IaC/GitOps sync for hosts (today: seed + inventory file).

## Remote deployment: SSH executor next to the gateway (OPS host)

Other agents reach SSH tools only through `https://mcp.svc.plus` with
`Authorization: Bearer <MCP_SERVICE_TOKEN>`; nobody needs a local private key.

- OPS role: CMDB host with `roles` containing `ops` (`infra.cmdb.query {"kind":"host","role":"ops"}`).
  Today `sit-k3s-v6` holds `["sit","ops"]` with `roles_temporary: true`; move the role by editing the
  seed or `hosts.json`, then move the gitops unit to that host's cluster.
- Pod layout (gitops `platform-mcp-sit`):
  - initContainer (gateway image): `-render-ssh-config /run/ssh-mcp/ssh-config.json` from CMDB into a
    shared `emptyDir`; `SSH_MCP_PRIVATE_KEY_PATH` points at the sidecar's Secret mount; optional
    `-ssh-host-map` (Vault-sourced `{hostname: address}`) resolves short names without committing IPs.
  - sidecar `ssh-mcp-server` (`node` image, `@fangjunjie/ssh-mcp-server` pinned version):
    `--mcp-transport http --http-host 127.0.0.1 --http-port 3000`, bearer `SSH_MCP_HTTP_TOKEN`. Its
    HTTP transport is stateless Streamable HTTP with JSON responses (no `Mcp-Session-Id`).
  - gateway backend:
    ```json
    {"name": "ssh", "capability": "runtime", "transport": "streamable-http",
     "url": "http://127.0.0.1:3000/mcp", "bearer_token_env": "SSH_MCP_HTTP_TOKEN",
     "allowed_tools": ["list-servers", "execute-command"],
     "disabled_tools": ["write-file", "upload", "download", "read-file", "job-*"]}
    ```
  - `bearer_token_env` (new) reads the backend token from an env var, so it can come from a Secret.
- Verified locally: gateway → ssh-mcp-server 1.11.0 HTTP mode lists only `ssh.execute-command` /
  `ssh.list-servers`; `ssh.write-file` is refused by gateway policy.
- Note: `list-servers` output shows the dial address, so host-map addresses become visible to
  authenticated agents at runtime (never in git/CMDB).
- Note: ssh-mcp-server does not verify SSH host keys; restrict egress and use a dedicated `auditor` key.
