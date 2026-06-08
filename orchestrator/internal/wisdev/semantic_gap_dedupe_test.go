package wisdev

import "testing"

func TestSemanticGapDedupeDetectsSynonymousQueries(t *testing.T) {
	accepted := []string{"sleep memory independent replication evidence"}
	if !semanticallyRedundantLoopQuery("sleep and memory independently replicated source", accepted, semanticGapDuplicateThreshold) {
		t.Fatal("expected synonym-normalized query to be treated as redundant")
	}
	if semanticallyRedundantLoopQuery("sleep memory contradictory trial evidence", accepted, semanticGapDuplicateThreshold) {
		t.Fatal("contradiction follow-up should remain distinct from replication follow-up")
	}
}
