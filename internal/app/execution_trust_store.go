package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"

	"github.com/nanaki-93/kogin/v2/internal/brand"
	"github.com/nanaki-93/kogin/v2/internal/storage"
)

const executionTrustPolicy = "project-execution-v1"

type rememberedExecutionTrust struct {
	Policy    string     `json:"policy"`
	Root      string     `json:"root"`
	ProjectID string     `json:"project_id"`
	Commands  [][]string `json:"commands"`
	Trusted   bool       `json:"trusted"`
}

// Consent is user-owned: a repository's metadata must never grant execution.
func executionTrustPath(root string) (string, error) {
	directory := brand.Getenv("KOGIN_TRUST_DIR")
	if directory == "" {
		configDir, err := brand.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("locate execution trust settings: %w", err)
		}
		directory = filepath.Join(configDir, "execution-trust")
	}
	if !filepath.IsAbs(directory) {
		return "", fmt.Errorf("execution trust settings directory must be absolute")
	}
	return filepath.Join(directory, fmt.Sprintf("%x.json", sha256.Sum256([]byte(root)))), nil
}

func readRememberedExecutionTrust(root string) (*rememberedExecutionTrust, error) {
	path, err := executionTrustPath(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read execution trust settings: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > 64*1024 {
		return nil, fmt.Errorf("execution trust settings must be a regular file under 64 KiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open execution trust settings: %w", err)
	}
	defer file.Close()
	var saved rememberedExecutionTrust
	decoder := json.NewDecoder(io.LimitReader(file, 64*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&saved); err != nil {
		return nil, fmt.Errorf("decode execution trust settings: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("execution trust settings must contain one JSON record")
	}
	if saved.Policy == "" || saved.Root == "" || saved.ProjectID == "" || len(saved.Commands) == 0 {
		return nil, fmt.Errorf("execution trust settings are incomplete")
	}
	return &saved, nil
}

func writeRememberedExecutionTrust(ctx context.Context, root, projectID string, commands [][]string, trusted bool) error {
	path, err := executionTrustPath(root)
	if err != nil {
		return err
	}
	data, err := json.Marshal(rememberedExecutionTrust{Policy: executionTrustPolicy, Root: root, ProjectID: projectID, Commands: commands, Trusted: trusted})
	if err != nil {
		return fmt.Errorf("encode execution trust settings: %w", err)
	}
	if err := storage.WriteFileAuthorized(path, data, 0600, ctx.Err); err != nil {
		return fmt.Errorf("save execution trust settings: %w", err)
	}
	return nil
}

// Caller owns executionTrust.mu. Restoration reads local facts only; it never
// runs a command, calls a provider or saves a new consent decision.
func (s *Service) restoreExecutionTrustLocked(ctx context.Context, root string, trust *ExecutionTrust) error {
	saved, err := readRememberedExecutionTrust(root)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	commands, err := s.projectCodeCommands(root, "")
	if err != nil {
		return err
	}
	if saved != nil && saved.Trusted && saved.Policy == executionTrustPolicy && saved.Root == root && saved.ProjectID == trust.ProjectID && reflect.DeepEqual(saved.Commands, commands) {
		hash, err := s.executionTrustWorkspace(ctx, root, trust)
		if err != nil {
			return err
		}
		s.executionTrust.project, s.executionTrust.revision, s.executionTrust.workspaceHash = trust.ProjectID, trust.ProjectRevision, hash
	}
	s.executionTrust.restorePending = false
	return nil
}
