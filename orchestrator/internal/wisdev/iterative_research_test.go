package wisdev

import (
	"context"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	wisdevpb "github.com/wisdev/wisdev-agent-os/orchestrator/proto/wisdev"
)

// --- IterativeResearch ---

func TestIterativeResearch_EmptyQueries_NoPanic(t *testing.T) {
	orig := ParallelSearch
	defer func() { ParallelSearch = orig }()
	ParallelSearch = func(ctx context.Context, _ redis.UniversalClient, query string, opts SearchOptions) (*MultiSourceResult, error) {
		return &MultiSourceResult{Papers: []Source{}}, nil
	}

	// Calling with an empty query slice must not panic (guards the
	// iterationLogs[len-1] index we fixed).
	result, err := IterativeResearch(context.Background(), []string{}, "sess-0", 1, 0.8)
	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.Empty(t, result.Papers)
	assert.Equal(t, 0.0, result.FinalCoverage)
	assert.Equal(t, 0.0, result.FinalReward)
}

func TestIterativeResearch_SingleIteration_ReturnsPapers(t *testing.T) {
	orig := ParallelSearch
	defer func() { ParallelSearch = orig }()
	ParallelSearch = func(ctx context.Context, _ redis.UniversalClient, query string, opts SearchOptions) (*MultiSourceResult, error) {
		return &MultiSourceResult{
			Papers: []Source{
				{ID: "p1", Title: "Paper One", DOI: "10.1/p1"},
				{ID: "p2", Title: "Paper Two", DOI: "10.1/p2"},
			},
		}, nil
	}

	result, err := IterativeResearch(context.Background(), []string{"neural networks"}, "sess-1", 1, 0.8)
	require.NoError(t, err)
	assert.Len(t, result.Iterations, 1)
	assert.Equal(t, 1, result.Iterations[0].Iteration)
	assert.NotEmpty(t, result.Papers)
	assert.Greater(t, result.FinalReward, 0.0)
	assert.Greater(t, result.FinalCoverage, 0.0)
}

func TestIterativeResearch_EarlyExitOnHighReward(t *testing.T) {
	orig := ParallelSearch
	defer func() { ParallelSearch = orig }()
	// Return many papers with DOIs so the PRM reward is high (>= threshold).
	papers := make([]Source, 20)
	for i := range papers {
		papers[i] = Source{ID: "p", Title: "P", DOI: "10.1/x"}
	}
	ParallelSearch = func(ctx context.Context, _ redis.UniversalClient, query string, opts SearchOptions) (*MultiSourceResult, error) {
		return &MultiSourceResult{Papers: papers}, nil
	}

	result, err := IterativeResearch(context.Background(), []string{"q1", "q2"}, "sess-2", 5, 0.1)
	require.NoError(t, err)
	// Should exit before maxIterations=5 when reward >= threshold=0.1.
	assert.Less(t, len(result.Iterations), 5)
}

func TestIterativeResearch_DefaultsApplied(t *testing.T) {
	orig := ParallelSearch
	defer func() { ParallelSearch = orig }()
	ParallelSearch = func(ctx context.Context, _ redis.UniversalClient, query string, opts SearchOptions) (*MultiSourceResult, error) {
		return &MultiSourceResult{Papers: []Source{}}, nil
	}

	// maxIterations=0 and coverageThreshold=0 must be defaulted to 3 and 0.8.
	result, err := IterativeResearch(context.Background(), []string{"test"}, "sess-3", 0, 0)
	require.NoError(t, err)
	// With empty search results reward stays low, so all 3 default iterations run.
	assert.LessOrEqual(t, len(result.Iterations), 3)
}

func TestIterativeResearch_IterationLogFields(t *testing.T) {
	orig := ParallelSearch
	defer func() { ParallelSearch = orig }()
	call := 0
	ParallelSearch = func(ctx context.Context, _ redis.UniversalClient, query string, opts SearchOptions) (*MultiSourceResult, error) {
		call++
		return &MultiSourceResult{Papers: []Source{{ID: "p", Title: "P"}}}, nil
	}

	result, err := IterativeResearch(context.Background(), []string{"query"}, "sess-4", 2, 0.99)
	require.NoError(t, err)
	require.NotEmpty(t, result.Iterations)

	first := result.Iterations[0]
	assert.Equal(t, 1, first.Iteration)
	assert.NotEmpty(t, first.QueriesAdded)
	// CoverageScore and PRMReward must be in [0, 1].
	assert.GreaterOrEqual(t, first.CoverageScore, 0.0)
	assert.LessOrEqual(t, first.PRMReward, 1.0)

	last := result.Iterations[len(result.Iterations)-1]
	assert.Equal(t, result.FinalCoverage, last.CoverageScore)
	assert.Equal(t, result.FinalReward, last.PRMReward)
}

// --- toProtoIterationLogs ---

func TestToProtoIterationLogs_Empty(t *testing.T) {
	out := toProtoIterationLogs(nil)
	assert.Empty(t, out)

	out = toProtoIterationLogs([]IterationLog{})
	assert.Empty(t, out)
}

func TestToProtoIterationLogs_Conversion(t *testing.T) {
	logs := []IterationLog{
		{Iteration: 1, QueriesAdded: []string{"q1", "q2"}, CoverageScore: 0.75, PRMReward: 0.82},
		{Iteration: 2, QueriesAdded: []string{"q3"}, CoverageScore: 0.9, PRMReward: 0.95},
	}

	out := toProtoIterationLogs(logs)
	require.Len(t, out, 2)

	assert.Equal(t, int32(1), out[0].Iteration)
	assert.Equal(t, []string{"q1", "q2"}, out[0].QueriesAdded)
	assert.InDelta(t, float32(0.75), out[0].CoverageScore, 0.001)
	assert.InDelta(t, float32(0.82), out[0].PrmReward, 0.001)

	assert.Equal(t, int32(2), out[1].Iteration)
	assert.Equal(t, []string{"q3"}, out[1].QueriesAdded)
	assert.InDelta(t, float32(0.9), out[1].CoverageScore, 0.001)
	assert.InDelta(t, float32(0.95), out[1].PrmReward, 0.001)
}

// --- searchGatewayServer.IterativeSearch ---

func TestSearchGatewayServer_IterativeSearch_EmptyQueries(t *testing.T) {
	origIR := IterativeResearch
	defer func() { IterativeResearch = origIR }()
	IterativeResearch = func(ctx context.Context, queries []string, sessionID string, maxIterations int, coverageThreshold float64) (*IterativeResearchResult, error) {
		return &IterativeResearchResult{
			Papers:        nil,
			Iterations:    nil,
			FinalCoverage: 0,
			FinalReward:   0,
		}, nil
	}

	srv := &searchGatewayServer{}
	resp, err := srv.IterativeSearch(context.Background(), &wisdevpb.IterativeSearchRequest{
		Queries:           []string{},
		SessionId:         "sess-grpc-0",
		MaxIterations:     1,
		CoverageThreshold: 0.8,
	})
	require.NoError(t, err)
	assert.Empty(t, resp.Papers)
	assert.Empty(t, resp.Iterations)
	assert.Equal(t, float32(0), resp.FinalCoverage)
	assert.Equal(t, float32(0), resp.FinalReward)
}

func TestSearchGatewayServer_IterativeSearch_Conversion(t *testing.T) {
	origIR := IterativeResearch
	defer func() { IterativeResearch = origIR }()
	IterativeResearch = func(ctx context.Context, queries []string, sessionID string, maxIterations int, coverageThreshold float64) (*IterativeResearchResult, error) {
		return &IterativeResearchResult{
			Papers: []Source{{ID: "p1", Title: "T1", DOI: "10.1/p1", Link: "https://ex.com/p1"}},
			Iterations: []IterationLog{
				{Iteration: 1, QueriesAdded: []string{"q"}, CoverageScore: 0.5, PRMReward: 0.6},
			},
			FinalCoverage: 0.5,
			FinalReward:   0.6,
		}, nil
	}

	srv := &searchGatewayServer{}
	resp, err := srv.IterativeSearch(context.Background(), &wisdevpb.IterativeSearchRequest{
		Queries:           []string{"neural networks"},
		SessionId:         "sess-grpc-1",
		MaxIterations:     3,
		CoverageThreshold: 0.8,
	})
	require.NoError(t, err)

	// Papers forwarded correctly.
	require.Len(t, resp.Papers, 1)
	assert.Equal(t, "p1", resp.Papers[0].Id)
	assert.Equal(t, "10.1/p1", resp.Papers[0].Doi)

	// Iterations converted via toProtoIterationLogs.
	require.Len(t, resp.Iterations, 1)
	assert.Equal(t, int32(1), resp.Iterations[0].Iteration)
	assert.InDelta(t, float32(0.5), resp.Iterations[0].CoverageScore, 0.001)
	assert.InDelta(t, float32(0.6), resp.Iterations[0].PrmReward, 0.001)

	// Final scores narrowed to float32.
	assert.InDelta(t, float32(0.5), resp.FinalCoverage, 0.001)
	assert.InDelta(t, float32(0.6), resp.FinalReward, 0.001)
}
