package wisdev

import (
	"context"
	"log"
	"log/slog"
	"net"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
	wisdevpb "github.com/wisdev-agent/wisdev-agent-os/orchestrator/proto/v2"

	"google.golang.org/grpc"
)

type searchGatewayServer struct {
	wisdevpb.UnimplementedSearchGatewayServer
	rdb redis.UniversalClient
}

func (s *searchGatewayServer) Search(ctx context.Context, req *wisdevpb.SearchRequest) (*wisdevpb.SearchResponse, error) {
	slog.Info("Received Search gRPC request", "query", req.Query, "limit", req.Limit)

	stage2Rerank := os.Getenv("WISDEV_STAGE2_RERANK") == "true"
	opts := SearchOptions{
		Limit:        int(req.Limit),
		ExpandQuery:  true,
		QualitySort:  true, // Default to true
		SkipCache:    false,
		Stage2Rerank: stage2Rerank,
	}

	result, err := ParallelSearch(ctx, s.rdb, req.Query, opts)
	if err != nil {
		return nil, err
	}

	var papers []*wisdevpb.SearchPaper
	for _, p := range result.Papers {
		id := p.ID
		if id == "" {
			if p.DOI != "" {
				id = p.DOI
			} else {
				id = p.Title
			}
		}
		papers = append(papers, &wisdevpb.SearchPaper{
			Id:     id,
			Title:  p.Title,
			Doi:    p.DOI,
			Link:   p.Link,
			Source: "multi-source",
		})
	}

	return &wisdevpb.SearchResponse{
		Papers: papers,
	}, nil
}

func (s *searchGatewayServer) IterativeSearch(ctx context.Context, req *wisdevpb.IterativeSearchRequest) (*wisdevpb.IterativeSearchResponse, error) {
	slog.Info("Received IterativeSearch gRPC request", "queries", req.Queries, "session_id", req.SessionId)

	result, err := IterativeResearch(
		ctx,
		req.Queries,
		req.SessionId,
		int(req.MaxIterations),
		float64(req.CoverageThreshold),
	)
	if err != nil {
		return nil, err
	}

	var papers []*wisdevpb.SearchPaper
	for _, p := range result.Papers {
		papers = append(papers, &wisdevpb.SearchPaper{
			Id:     p.ID,
			Title:  p.Title,
			Doi:    p.DOI,
			Link:   p.Link,
			Source: "iterative-source",
		})
	}

	var iterations []*wisdevpb.IterationLog
	for _, iter := range result.Iterations {
		iterations = append(iterations, &wisdevpb.IterationLog{
			Iteration:     int32(iter.Iteration),
			QueriesAdded:  iter.QueriesAdded,
			CoverageScore: float32(iter.CoverageScore),
			PrmReward:     float32(iter.PRMReward),
		})
	}

	return &wisdevpb.IterativeSearchResponse{
		Papers:        papers,
		Iterations:    iterations,
		FinalCoverage: float32(result.FinalCoverage),
		FinalReward:   float32(result.FinalReward),
	}, nil
}

func (s *searchGatewayServer) ReRankResults(ctx context.Context, req *wisdevpb.ReRankRequest) (*wisdevpb.ReRankResponse, error) {
	started := time.Now()
	query := req.GetQuery()
	domain := req.GetDomain()
	topK := int(req.GetTopK())

	input := req.GetPapers()
	sources := make([]Source, 0, len(input))
	for _, row := range input {
		if row == nil {
			continue
		}
		sources = append(sources, Source{
			ID:            row.GetId(),
			Title:         row.GetTitle(),
			Summary:       row.GetAbstract(),
			Link:          row.GetLink(),
			DOI:           row.GetDoi(),
			Source:        row.GetSource(),
			Score:         float64(row.GetScore()),
			CitationCount: int(row.GetCitationCount()),
		})
	}

	reranked := rerankPapersStage2(ctx, query, sources, domain, topK)
	out := make([]*wisdevpb.ReRankDocument, 0, len(reranked))
	for _, row := range reranked {
		out = append(out, &wisdevpb.ReRankDocument{
			Id:            row.ID,
			Title:         row.Title,
			Doi:           row.DOI,
			Link:          row.Link,
			Source:        row.Source,
			Abstract:      row.Summary,
			CitationCount: int32(row.CitationCount),
			Score:         float32(row.Score),
		})
	}

	return &wisdevpb.ReRankResponse{
		Papers:       out,
		RerankTimeMs: int32(time.Since(started).Milliseconds()),
		RerankMethod: "go-stage2-cross-encoder-deprecated",
	}, nil
}

func (s *searchGatewayServer) StreamSearch(req *wisdevpb.SearchRequest, stream wisdevpb.SearchGateway_StreamSearchServer) error {
	resp, err := s.Search(stream.Context(), req)
	if err != nil {
		return err
	}
	total := len(resp.Papers)
	for i, paper := range resp.Papers {
		if err := stream.Send(&wisdevpb.SearchUpdate{
			Paper: paper,
			Progress: &wisdevpb.Progress{
				Completed: int32(i + 1),
				Total:     int32(total),
			},
		}); err != nil {
			return err
		}
	}
	return nil
}

func StartGRPCServer(port string, gw *AgentGateway, rdb redis.UniversalClient) error {
	lis, err := net.Listen("tcp", port)
	if err != nil {
		return err
	}
	s := grpc.NewServer()
	wisdevpb.RegisterSearchGatewayServer(s, &searchGatewayServer{rdb: rdb})
	wisdevpb.RegisterAgentGatewayServer(s, &agentGatewayGRPCServer{
		gateway: gw,
	})

	log.Printf("Starting gRPC Search Gateway Server on %v", lis.Addr())
	if err := s.Serve(lis); err != nil {
		return err
	}
	return nil
}
