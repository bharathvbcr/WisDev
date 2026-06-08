package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGeneratorMain(t *testing.T) {
	originalWD, err := os.Getwd()
	require.NoError(t, err)

	tempDir := t.TempDir()
	require.NoError(t, os.Chdir(tempDir))
	t.Cleanup(func() {
		_ = os.Chdir(originalWD)
	})

	require.NoError(t, os.WriteFile("go.mod", []byte("module tempmod\n\ngo 1.25.0\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join("proto", "wisdev"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join("proto", "llm"), 0o755))

	wisdevContent := `package wisdev

func _WisDevService_Unary_Handler(srv any, ctx any, dec func(any) error, interceptor any) (any, error) { return nil, nil }
func _WisDevService_Stream_Handler(srv any, stream any) error { return nil }
`
	llmContent := `package llm

func _LLMService_Unary_Handler(srv any, ctx any, dec func(any) error, interceptor any) (any, error) { return nil, nil }
func _LLMService_Stream_Handler(srv any, stream any) error { return nil }
`
	require.NoError(t, os.WriteFile(filepath.Join("proto", "wisdev", "wisdev_grpc.pb.go"), []byte(wisdevContent), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join("proto", "llm", "llm_grpc.pb.go"), []byte(llmContent), 0o644))

	main()

	wisdevAuto, err := os.ReadFile(filepath.Join("proto", "wisdev", "auto_coverage_test.go"))
	require.NoError(t, err)
	llmAuto, err := os.ReadFile(filepath.Join("proto", "llm", "auto_coverage_test.go"))
	require.NoError(t, err)

	assert.Contains(t, string(wisdevAuto), "TestAutoCoverageGenerated")
	assert.Contains(t, string(llmAuto), "TestAutoCoverageGenerated")
}
