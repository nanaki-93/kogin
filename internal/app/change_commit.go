package app

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/nanaki-93/kogin/v2/internal/app/gitops"
)

type ChangeCommit = gitops.ChangeCommit

func validateCommitMessage(message string) error {
	if strings.TrimSpace(message) == "" || len(message) > 4096 || !utf8.ValidString(message) || strings.ContainsRune(message, 0) {
		return fmt.Errorf("provide a non-empty commit message of at most 4096 bytes")
	}
	return nil
}

func gitFileMode(mode uint32) string {
	if mode&0111 != 0 {
		return "100755"
	}
	return "100644"
}
