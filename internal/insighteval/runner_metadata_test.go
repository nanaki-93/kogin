package insighteval

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEngineeringInsightRunnerMetadataReplacesCompactPrivateJSON(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "campaign.json")
	for _, requests := range []int{1, 2} {
		if err := storeRunnerJSON(path, engineeringInsightCampaign{DevelopmentRequests: requests}); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != `{"development_requests":2,"qualification_requests":0}` {
		t.Fatalf("metadata = %q, %v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("metadata permissions = %v", info.Mode().Perm())
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 || entries[0].Name() != "campaign.json" {
		t.Fatalf("metadata directory = %v, %v", entries, err)
	}
}

type failingRunnerMetadata struct{ err error }

func (value failingRunnerMetadata) MarshalJSON() ([]byte, error) { return nil, value.err }

func TestEngineeringInsightRunnerMetadataMarshalFailureRetainsPriorData(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "campaign.json")
	prior := `{"development_requests":1,"qualification_requests":0}`
	if err := os.WriteFile(path, []byte(prior), 0600); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("private source and provider response")
	err := storeRunnerJSON(path, failingRunnerMetadata{err: failure})
	if err == nil || err.Error() != "write evaluation run" || fmt.Sprintf("%+v", err) != "write evaluation run" {
		t.Fatalf("public write error = %v", err)
	}
	if !errors.Is(err, failure) || !strings.Contains(errors.Unwrap(err).Error(), "marshal evaluation metadata") {
		t.Fatalf("missing internal marshal context: %v", errors.Unwrap(err))
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != prior {
		t.Fatalf("prior metadata = %q, %v", data, readErr)
	}
	entries, readErr := os.ReadDir(directory)
	if readErr != nil || len(entries) != 1 {
		t.Fatalf("metadata directory after failure = %v, %v", entries, readErr)
	}
}

func TestEngineeringInsightRunnerMetadataRequiresExistingDirectory(t *testing.T) {
	for _, parentIsFile := range []bool{false, true} {
		t.Run(fmt.Sprintf("parentIsFile=%t", parentIsFile), func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "private-evaluation-directory")
			if parentIsFile {
				if err := os.WriteFile(directory, []byte("prior"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := storeRunnerJSON(filepath.Join(directory, "campaign.json"), engineeringInsightCampaign{})
			if err == nil || err.Error() != "write evaluation run" {
				t.Fatalf("public directory error = %v", err)
			}
			if parentIsFile {
				data, readErr := os.ReadFile(directory)
				if readErr != nil || string(data) != "prior" {
					t.Fatalf("prior file = %q, %v", data, readErr)
				}
			} else {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("missing internal directory error: %v", errors.Unwrap(err))
				}
				if _, statErr := os.Stat(directory); !os.IsNotExist(statErr) {
					t.Fatalf("missing directory was created: %v", statErr)
				}
			}
		})
	}
}

func TestEngineeringInsightRunnerMetadataReplacementFailureCleansTemp(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "campaign.json")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	priorPath := filepath.Join(path, "prior.json")
	if err := os.WriteFile(priorPath, []byte("prior"), 0600); err != nil {
		t.Fatal(err)
	}
	err := storeRunnerJSON(path, engineeringInsightCampaign{})
	if err == nil || err.Error() != "write evaluation run" {
		t.Fatalf("public replacement error = %v", err)
	}
	if cause := errors.Unwrap(err); cause == nil || !strings.Contains(cause.Error(), "replace metadata") {
		t.Fatalf("missing internal replacement context: %v", cause)
	}
	data, readErr := os.ReadFile(priorPath)
	if readErr != nil || string(data) != "prior" {
		t.Fatalf("prior destination content = %q, %v", data, readErr)
	}
	entries, readErr := os.ReadDir(directory)
	if readErr != nil || len(entries) != 1 || entries[0].Name() != "campaign.json" {
		t.Fatalf("metadata directory after failure = %v, %v", entries, readErr)
	}
}

func TestEngineeringInsightRunnerPrivateArtifactCollisionRetainsPriorData(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "response.json")
	if err := storePrivateArtifact(path, []byte("prior response")); err != nil {
		t.Fatal(err)
	}
	if err := storePrivateArtifact(path, []byte("next response")); !errors.Is(err, os.ErrExist) {
		t.Fatalf("immutable artifact collision = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "prior response" {
		t.Fatalf("prior artifact = %q, %v", data, err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 || entries[0].Name() != "response.json" {
		t.Fatalf("artifact directory after collision = %v, %v", entries, err)
	}
}
