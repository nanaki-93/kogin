package main

import (
	"fmt"
	"os"
	"testing"
)

// Fixture consent must never read or modify the developer's saved decisions.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "kogin-trust-tests-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.Setenv("KOGIN_TRUST_DIR", dir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	if err := os.RemoveAll(dir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = 1
	}
	os.Exit(code)
}
