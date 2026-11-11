package sshconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ai-workspace-infra/Infrastructure-MCP-Server/internal/cmdb"
	"github.com/ai-workspace-infra/Infrastructure-MCP-Server/internal/graph"
)

var ipLike = regexp.MustCompile(`\b\d{1,3}(\.\d{1,3}){3}\b`)

func TestRenderFromCMDBSeed(t *testing.T) {
	dir := t.TempDir()
	store := cmdb.NewStore(graph.NewStore(), dir)
	targets := store.SSHTargets()
	if len(targets) != 10 {
		t.Fatalf("expected 10 seeded SSH targets (2 k3s + 8 edge), got %d", len(targets))
	}
	conns, refs, err := Render(targets, DefaultPolicy(), Options{PrivateKeyPath: "/home/op/.ssh/auditor_ed25519"})
	if err != nil {
		t.Fatal(err)
	}
	if len(conns) != 10 {
		t.Fatalf("want 10 connections, got %d", len(conns))
	}
	names := map[string]Connection{}
	for _, c := range conns {
		names[c.Name] = c
		if c.Username != "auditor" {
			t.Errorf("%s: want user auditor, got %s", c.Name, c.Username)
		}
		if ipLike.MatchString(c.Host) {
			t.Errorf("%s: host must be a hostname, got %s", c.Name, c.Host)
		}
		if len(c.CommandWhitelist) == 0 || c.Pty {
			t.Errorf("%s: policy not applied", c.Name)
		}
	}
	if names["sit-k3s-v6"].Host != "sit-k3s-v6.svc.plus" || names["prod-k3s-v6"].Host != "prod-k3s-v6.svc.plus" {
		t.Errorf("k3s nodes must dial their GitOps-declared FQDNs, got %s / %s", names["sit-k3s-v6"].Host, names["prod-k3s-v6"].Host)
	}
	for _, n := range []string{"sit-k3s-v6", "prod-k3s-v6", "us-xconnect-los-angeles-custom-01", "jp-free-selfhost-prod-v2"} {
		if _, ok := names[n]; !ok {
			t.Errorf("missing connection %s", n)
		}
	}
	if len(refs) != 1 || refs[0] != cmdb.DefaultSSHVaultRef {
		t.Fatalf("unexpected vault refs %v", refs)
	}

	prodOnly, _, err := Render(store.SSHTargets("sit"), DefaultPolicy(), Options{AgentSocket: "/tmp/agent.sock"})
	if err != nil || len(prodOnly) != 1 || prodOnly[0].Name != "sit-k3s-v6" || prodOnly[0].Agent == "" {
		t.Fatalf("environment filter failed: %v %#v", err, prodOnly)
	}

	out := filepath.Join(dir, "out", "ssh-config.json")
	if err := WriteFile(out, conns); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(out)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("want 0600, got %v", info.Mode().Perm())
	}
	data, _ := os.ReadFile(out)
	var decoded []map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil || decoded[0]["commandWhitelist"] == nil {
		t.Fatalf("rendered file not in ssh-mcp-server format: %v", err)
	}
	if ipLike.Match(data) {
		t.Fatal("rendered config must not contain IP addresses")
	}
}

func TestRenderRequiresCredentialAndWhitelist(t *testing.T) {
	res := []cmdb.Resource{{ID: "host/sit/a", SSH: &cmdb.SSHAccess{Host: "a", User: "auditor", ConnectionName: "a"}}}
	if _, _, err := Render(res, DefaultPolicy(), Options{}); err == nil {
		t.Fatal("expected missing credential error")
	}
	if _, _, err := Render(res, Policy{}, Options{AgentSocket: "/x"}); err == nil {
		t.Fatal("expected missing whitelist error")
	}
}

func TestInventoryFileOverridesAndExtends(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "cmdb"), 0o755); err != nil {
		t.Fatal(err)
	}
	inv := `[
	  {"hostname":"sg-xconnect.svc.plus","environment":"prod","role":"edge-agent","provider":"vultr","inspect":false},
	  {"hostname":"kr-xconnect.svc.plus","environment":"prod","role":"edge-agent","provider":"aws","ssh_user":"auditor"}
	]`
	if err := os.WriteFile(filepath.Join(dir, "cmdb", "hosts.json"), []byte(inv), 0o644); err != nil {
		t.Fatal(err)
	}
	store := cmdb.NewStore(graph.NewStore(), dir)
	targets := store.SSHTargets()
	got := map[string]bool{}
	for _, r := range targets {
		got[r.SSH.ConnectionName] = true
	}
	if got["sg-xconnect"] {
		t.Error("inspect:false host must not be an SSH target")
	}
	if !got["kr-xconnect"] {
		t.Error("inventory file host missing")
	}
	if len(targets) != 10 {
		t.Errorf("want 10 targets (10 seed -1 +1), got %d", len(targets))
	}
	hosts := store.Resources("", "", "host", "")
	if len(hosts) != 11 {
		t.Errorf("want 11 host resources, got %d", len(hosts))
	}
	for _, h := range hosts {
		if strings.Contains(h.ID, "sg-xconnect") && h.Provider != "vultr" {
			t.Error("inventory override not applied")
		}
	}
}

func TestBaseDirFromEnv(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "cmdb"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "cmdb", "hosts.json"), []byte(`[{"hostname":"env-only.svc.plus","environment":"sit","role":"test","provider":"x"}]`), 0o644)
	t.Setenv("INFRA_CMDB_BASE_DIR", dir)
	store := cmdb.NewStore(graph.NewStore(), "")
	found := false
	for _, r := range store.SSHTargets("sit") {
		if r.SSH.Host == "env-only.svc.plus" {
			found = true
		}
	}
	if !found {
		t.Fatal("INFRA_CMDB_BASE_DIR not honoured")
	}
}

func TestOpsRole(t *testing.T) {
	store := cmdb.NewStore(graph.NewStore(), t.TempDir())
	var ops []cmdb.Resource
	for _, r := range store.Resources("", "", "host", "") {
		if cmdb.HasRole(r, "ops") {
			ops = append(ops, r)
		}
	}
	if len(ops) != 1 || ops[0].SSH.ConnectionName != "sit-k3s-v6" || ops[0].Attributes["roles_temporary"] != true {
		t.Fatalf("expected sit-k3s-v6 as the (temporary) ops host, got %#v", ops)
	}
	if !cmdb.HasRole(ops[0], "sit") || !cmdb.HasRole(ops[0], "k3s-node") {
		t.Fatal("ops host must keep its other roles")
	}

}
