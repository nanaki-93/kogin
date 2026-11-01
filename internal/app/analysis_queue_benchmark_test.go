package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nanaki-93/kasuri/v2/internal/project"
)

func BenchmarkAnalysisSourceFreshness(b *testing.B) {
	for _, files := range []int{100, 1000} {
		b.Run(fmt.Sprintf("%d_files_8KiB_each", files), func(b *testing.B) {
			root := b.TempDir()
			data := []byte("package sample\n// " + strings.Repeat("x", 8192-18) + "\n")
			for i := 0; i < files; i++ {
				if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("file%04d.go", i)), data, 0600); err != nil {
					b.Fatal(err)
				}
			}
			index, err := project.BuildIndex(root, "benchmark", "revision")
			if err != nil {
				b.Fatal(err)
			}
			policy, err := project.NewContextPolicy(root)
			if err != nil {
				b.Fatal(err)
			}
			plan := &AnalysisRunPreview{}
			for _, file := range index.Files {
				plan.Files = append(plan.Files, AnalysisPlannedFile{AnalysisFileIdentity: AnalysisFileIdentity{Path: file.Path, ContentHash: file.ContentHash, Language: file.Language}, SizeBytes: file.SizeBytes})
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := validateAnalysisFiles(context.Background(), root, plan, index, policy, true); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
