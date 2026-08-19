// test/integration/logger_helper_test.go
//go:build integration

package integration

import (
	"testing"

	"go.uber.org/zap"
)

func testLogger(t *testing.T) *zap.Logger {
	t.Helper()
	l, err := zap.NewDevelopment()
	if err != nil {
		t.Fatalf("failed to build test logger: %v", err)
	}
	return l
}
