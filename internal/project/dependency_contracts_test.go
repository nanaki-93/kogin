package project

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveDependencyContractsExtractsCalleeChainStateAndPeerAPI(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.test/vaultapp\n\ngo 1.22\n",
		"cmd/master.go": `package cmd

import "example.test/vaultapp/internal/vault"

func RotateMaster(pass []byte) error {
	derived := vault.DeriveMasterKey(pass)
	vault.SetMasterKey(derived)
	if err := vault.SaveStore(nil); err != nil {
		return err
	}
	_, err := vault.LoadStore()
	return err
}
`,
		"cmd/unused_helper.go": `package cmd

func UncalledSibling() {
	println("sibling secret literal")
}
`,
		"cmd/master_test.go": `package cmd

import "testing"

func TestRotateMaster(t *testing.T) {
	_ = RotateMaster([]byte("test-only-never-in-prompt"))
}
`,
		"internal/vault/vault.go": `package vault

import "errors"

var sessionSecret []byte

// SetMasterKey sets the active master password in memory for deriving the store encryption key.
func SetMasterKey(password []byte) {
	sessionSecret = append([]byte(nil), password...)
}

func DeriveMasterKey(raw []byte) []byte {
	return append([]byte("dk:"), raw...)
}

func SaveStore(data []byte) error {
	return writeEncrypted(data)
}

func writeEncrypted(data []byte) error {
	return encryptWithSecret(data)
}

func encryptWithSecret(data []byte) error {
	_ = DeriveMasterKey(sessionSecret)
	return nil
}

func LoadStore() ([]byte, error) {
	return decryptStore()
}

func decryptStore() ([]byte, error) {
	if len(sessionSecret) == 0 {
		return nil, errors.New("master key required")
	}
	return []byte("ok"), nil
}

// UnlockStore prompts once, validates, and sets the session master password via SetMasterKey.
func UnlockStore(raw []byte) error {
	SetMasterKey(raw)
	return nil
}
`,
	}
	for rel, content := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	index, err := BuildIndex(root, "vault-proj", "rev-1")
	if err != nil {
		t.Fatal(err)
	}
	target := functionContextFile(index, "cmd/master.go")
	if target == nil {
		t.Fatal("target cmd/master.go not indexed")
	}
	info, err := GetFileInfo(root, "cmd/master.go")
	if err != nil {
		t.Fatal(err)
	}

	contracts, err := ResolveDependencyContracts(context.Background(), root, index, *target, info.Content)
	if err != nil {
		t.Fatalf("ResolveDependencyContracts failed: %v", err)
	}

	for _, want := range []string{
		"SetMasterKey(password []byte)",
		"sets the active master password in memory for deriving the store encryption key",
		"sessionSecret",
		"encryptWithSecret",
		"decryptStore",
		"Related exported signatures in internal/vault:",
		"func UnlockStore(raw []byte) error",
	} {
		if !strings.Contains(contracts.Text, want) {
			t.Fatalf("contracts missing %q:\n%s", want, contracts.Text)
		}
	}
	for _, forbid := range []string{
		"sibling secret literal",
		"TestRotateMaster",
		"test-only-never-in-prompt",
	} {
		if strings.Contains(contracts.Text, forbid) {
			t.Fatalf("contracts leaked forbidden content %q:\n%s", forbid, contracts.Text)
		}
	}
	if len(contracts.Files) != 1 || contracts.Files[0].Path != "internal/vault/vault.go" || contracts.Files[0].Hash == "" || !contracts.Files[0].Truncated {
		t.Fatalf("contracts files = %+v, want [internal/vault/vault.go]", contracts.Files)
	}
}

func TestResolveDependencyContractsHonorsContextPolicyAndTypeScriptImports(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		".gitignore": "src/ignored.ts\n",
		"package.json": `{
  "name": "vault-cli"
}`,
		"src/cli.ts": `import { encryptRecord } from './lib/crypto';
import { ignoredHelper } from './ignored';

export function run(raw: string) {
  ignoredHelper();
  return encryptRecord(raw);
}
`,
		"src/lib/crypto.ts": `// Derive and seal a record payload.
export function encryptRecord(plaintext: string): string {
  return plaintext;
}
`,
		"src/ignored.ts": `export function ignoredHelper(): string {
  return "must-not-leak-ignored";
}
`,
	}
	for rel, content := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	index, err := BuildIndex(root, "vault-proj", "rev-1")
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an index that still carries a file later excluded by .gitignore
	// to prove ResolveDependencyContracts re-enforces ContextPolicy itself.
	index.Files = append(index.Files, IndexFile{
		Path: "src/ignored.ts", Language: "TypeScript", SizeBytes: int64(len(files["src/ignored.ts"])), ContentHash: "stale-hash",
	})
	target := functionContextFile(index, "src/cli.ts")
	if target == nil {
		t.Fatal("target src/cli.ts not indexed")
	}
	info, err := GetFileInfo(root, "src/cli.ts")
	if err != nil {
		t.Fatal(err)
	}

	contracts, err := ResolveDependencyContracts(context.Background(), root, index, *target, info.Content)
	if err != nil {
		t.Fatalf("ResolveDependencyContracts failed: %v", err)
	}
	if !strings.Contains(contracts.Text, "function encryptRecord(plaintext: string)") {
		t.Fatalf("expected TS dependency contract, got:\n%s", contracts.Text)
	}
	if strings.Contains(contracts.Text, "must-not-leak-ignored") || strings.Contains(contracts.Text, "ignoredHelper") {
		t.Fatalf("excluded file leaked into contracts:\n%s", contracts.Text)
	}
	if len(contracts.Excluded) == 0 || contracts.Excluded[0].Path != "src/ignored.ts" {
		t.Fatalf("expected exclusion record for src/ignored.ts, got %+v", contracts.Excluded)
	}
	if len(contracts.Files) != 1 || contracts.Files[0].Path != "src/lib/crypto.ts" {
		t.Fatalf("contracts files = %+v, want [src/lib/crypto.ts]", contracts.Files)
	}
}
