package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/durandom/token-burn/internal/provider"
	"github.com/durandom/token-burn/internal/store"
)

func TestCreditsCommandReadsMonetaryBalance(t *testing.T) {
	dir := t.TempDir()
	database := filepath.Join(dir, "state.db")
	cfg := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfg, []byte(fmt.Sprintf("database_path = %q\n[[accounts]]\nprovider = \"claude-platform\"\nid = \"console\"\n", database)), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	expiry := time.Now().UTC().Add(time.Hour)
	if err := db.InsertSnapshot(context.Background(), provider.Snapshot{Provider: "claude-platform", AccountID: "console", ObservedAt: time.Now(), CreditBalance: &provider.CreditBalance{OrganizationID: "org", Balance: provider.Money{AmountMinor: 21776, Currency: "USD", Exponent: 2}, Grants: []provider.CreditGrant{{Program: "subscription_api_credit", Remaining: provider.Money{AmountMinor: 20000, Currency: "USD", Exponent: 2}, ExpiresAt: &expiry}}}}, store.InsertOptions{}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	for _, args := range [][]string{{"credits"}, {"credits", "--json"}, {"credits", "--provider", "claude"}} {
		cmd := NewRootCommand(BuildInfo{})
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs(append([]string{"--config", cfg}, args...))
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		want := "$217.76"
		if len(args) > 1 && args[1] == "--json" {
			want = `"amount_minor": 21776`
		}
		if len(args) > 1 && args[1] == "--provider" {
			want = "No stored"
		}
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q: %s", want, out.String())
		}
	}
}

func TestAuthRequiresExplicitAccountWhenAmbiguous(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[[accounts]]\nprovider='claude-platform'\nid='one'\n[[accounts]]\nprovider='claude-platform'\nid='two'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := NewRootCommand(BuildInfo{})
	cmd.SetArgs([]string{"--config", path, "auth", "claude-platform"})
	cmd.SetIn(strings.NewReader("synthetic"))
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "select --account") {
		t.Fatalf("unexpected error: %v", err)
	}
}
