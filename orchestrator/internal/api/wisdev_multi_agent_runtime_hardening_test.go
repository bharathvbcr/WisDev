package api

import (
	"context"
	"strings"
	"testing"
)

func TestExecuteWisdevMultiAgentSwarmRequiresUnifiedRuntime(t *testing.T) {
	resp, err := executeWisdevMultiAgentSwarm(context.Background(), nil, "memory consolidation", "", 2, true)
	if err == nil {
		t.Fatalf("expected unified runtime availability error, got response %#v", resp)
	}
	if !strings.Contains(err.Error(), "wisdev_unified_runtime_unavailable") {
		t.Fatalf("expected structured unified runtime error, got %v", err)
	}
}
