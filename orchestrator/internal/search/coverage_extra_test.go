package search

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/redis/go-redis/v9"
)

type redisHarness struct {
	mu     sync.Mutex
	values map[string]string
}

func newRedisHarness() *redisHarness {
	return &redisHarness{values: map[string]string{}}
}

func (h *redisHarness) serve(conn net.Conn) {
	defer conn.Close()

	reader := bufio.NewReader(conn)
	for {
		cmd, err := readRedisCommand(reader)
		if err != nil {
			return
		}
		if len(cmd) == 0 {
			continue
		}

		switch strings.ToUpper(cmd[0]) {
		case "GET":
			if len(cmd) < 2 {
				_, _ = io.WriteString(conn, "$-1\r\n")
				continue
			}
			h.mu.Lock()
			val, ok := h.values[cmd[1]]
			h.mu.Unlock()
			if !ok {
				_, _ = io.WriteString(conn, "$-1\r\n")
				continue
			}
			_, _ = fmt.Fprintf(conn, "$%d\r\n%s\r\n", len(val), val)
		case "SET":
			if len(cmd) >= 3 {
				h.mu.Lock()
				h.values[cmd[1]] = cmd[2]
				h.mu.Unlock()
			}
			_, _ = io.WriteString(conn, "+OK\r\n")
		case "PING":
			_, _ = io.WriteString(conn, "+PONG\r\n")
		default:
			_, _ = io.WriteString(conn, "+OK\r\n")
		}
	}
}

func readRedisCommand(r *bufio.Reader) ([]string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, nil
	}
	if !strings.HasPrefix(line, "*") {
		return nil, fmt.Errorf("unexpected redis frame: %q", line)
	}
	n, err := strconv.Atoi(strings.TrimPrefix(line, "*"))
	if err != nil {
		return nil, err
	}

	args := make([]string, 0, n)
	for i := 0; i < n; i++ {
		prefix, err := r.ReadByte()
		if err != nil {
			return nil, err
		}
		if prefix != '$' {
			return nil, fmt.Errorf("unexpected arg prefix: %q", prefix)
		}
		sizeLine, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		sizeLine = strings.TrimSpace(sizeLine)
		size, err := strconv.Atoi(sizeLine)
		if err != nil {
			return nil, err
		}
		buf := make([]byte, size+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		args = append(args, string(buf[:size]))
	}
	return args, nil
}

func newRedisClient(t *testing.T, h *redisHarness) *redis.Client {
	t.Helper()
	return redis.NewClient(&redis.Options{
		Addr:     "redis-test",
		Protocol: 2,
		Dialer: func(ctx context.Context, network, addr string) (net.Conn, error) {
			client, server := net.Pipe()
			go h.serve(server)
			return client, nil
		},
	})
}

type testAuthorSearcher struct {
	name     string
	err      error
	papers   []Paper
	called   int
	tools    []string
	failOnce bool
}

func (p *testAuthorSearcher) Name() string      { return p.name }
func (p *testAuthorSearcher) Domains() []string { return []string{"general"} }
func (p *testAuthorSearcher) Healthy() bool     { return true }
func (p *testAuthorSearcher) Tools() []string   { return p.tools }
func (p *testAuthorSearcher) Search(context.Context, string, SearchOpts) ([]Paper, error) {
	return nil, nil
}
func (p *testAuthorSearcher) SearchByAuthor(context.Context, string, int) ([]Paper, error) {
	p.called++
	return p.papers, p.err
}

type testPaperLookup struct {
	name   string
	err    error
	paper  *Paper
	tools  []string
	called int
}

func (p *testPaperLookup) Name() string      { return p.name }
func (p *testPaperLookup) Domains() []string { return []string{"general"} }
func (p *testPaperLookup) Healthy() bool     { return true }
func (p *testPaperLookup) Tools() []string   { return p.tools }
func (p *testPaperLookup) Search(context.Context, string, SearchOpts) ([]Paper, error) {
	return nil, nil
}
func (p *testPaperLookup) SearchByPaperID(context.Context, string) (*Paper, error) {
	p.called++
	return p.paper, p.err
}

type testCitationProvider struct {
	name      string
	papers    []Paper
	err       error
	healthy   bool
	called    int
	getCalled int
}

func (p *testCitationProvider) Name() string      { return p.name }
func (p *testCitationProvider) Domains() []string { return []string{"general"} }
func (p *testCitationProvider) Healthy() bool     { return p.healthy }
func (p *testCitationProvider) Tools() []string   { return nil }
func (p *testCitationProvider) Search(context.Context, string, SearchOpts) ([]Paper, error) {
	return nil, nil
}
func (p *testCitationProvider) GetCitations(context.Context, string, int) ([]Paper, error) {
	p.getCalled++
	return p.papers, p.err
}

func TestCacheHelpers(t *testing.T) {
	t.Parallel()

	key := getCacheKey("q", SearchOpts{Domain: "cs", Limit: 7, YearFrom: 2001, YearTo: 2005, Sources: []string{"pubmed", "arxiv"}})
	if key != "search:q:d:cs:l:7:yf:2001:yt:2005:s:arxiv,pubmed" {
		t.Fatalf("unexpected cache key: %s", key)
	}

	if _, ok := checkCache(context.Background(), nil, "missing"); ok {
		t.Fatal("expected nil cache client to miss")
	}
	setCache(context.Background(), nil, "missing", SearchResult{})
	setCache(context.Background(), nil, "missing", SearchResult{Papers: []Paper{{ID: "1"}}})
}

func TestListToolDefinitionsIncludesRetrievalSchemas(t *testing.T) {
	tools := ListToolDefinitions()
	if len(tools) == 0 {
		t.Fatal("expected retrieval tool definitions")
	}
	byName := map[string]ToolDefinition{}
	for _, tool := range tools {
		byName[tool.Name] = tool
		if !tool.ReadOnly {
			t.Fatalf("expected retrieval tool %s to be read-only", tool.Name)
		}
		if !json.Valid(tool.Schema) {
			t.Fatalf("tool %s has invalid schema: %s", tool.Name, string(tool.Schema))
		}
	}
	if _, ok := byName["wisdevSearchPapers"]; !ok {
		t.Fatal("missing wisdevSearchPapers tool")
	}
	if len(byName["wisdevSearchPapers"].Aliases) == 0 {
		t.Fatal("expected retrieval tool aliases")
	}
	if _, ok := byName["paper_lookup"]; !ok {
		t.Fatal("missing paper_lookup tool")
	}
	if _, ok := byName["author_lookup"]; !ok {
		t.Fatal("missing author_lookup tool")
	}
}

func TestHandleToolSearch(t *testing.T) {
	t.Run("paper retrieval success with MCP-style tool", func(t *testing.T) {
		reg := NewProviderRegistry()
		provider := &mockProvider{
			name:    "semantic_scholar",
			healthy: true,
			papers:  []Paper{{ID: "p1", Title: "Retrieved Paper", Source: "semantic_scholar"}},
		}
		reg.Register(provider)

		res, err := HandleToolSearch(context.Background(), reg, "wisdevSearchPapers", map[string]any{
			"query":       "graph retrieval",
			"limit":       float64(100),
			"sources":     []any{" semantic_scholar ", "semantic_scholar"},
			"yearFrom":    float64(2020),
			"yearTo":      float64(2024),
			"qualitySort": true,
			"skipCache":   true,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(res.Papers) != 1 || res.Papers[0].ID != "p1" {
			t.Fatalf("unexpected retrieval result: %+v", res.Papers)
		}
		if provider.query != "graph retrieval" {
			t.Fatalf("expected query to reach provider, got %q", provider.query)
		}
		if provider.opts.Limit != maxToolSearchPapersLimit || provider.opts.YearFrom != 2020 || provider.opts.YearTo != 2024 {
			t.Fatalf("unexpected opts: %+v", provider.opts)
		}
		if len(provider.opts.Sources) != 1 || provider.opts.Sources[0] != "semantic_scholar" {
			t.Fatalf("expected deduped sources, got %+v", provider.opts.Sources)
		}
	})

	t.Run("paper retrieval alias and missing query", func(t *testing.T) {
		reg := NewProviderRegistry()
		provider := &mockProvider{name: "semantic_scholar", healthy: true, papers: []Paper{{ID: "alias"}}}
		reg.Register(provider)
		res, err := HandleToolSearch(context.Background(), reg, "research.retrievePapers", map[string]any{
			"query":               "alias path",
			"retrievalStrategies": []any{"semantic scholar", "not-a-provider"},
		})
		if err != nil {
			t.Fatalf("unexpected alias error: %v", err)
		}
		if len(res.Papers) != 1 || res.Papers[0].ID != "alias" {
			t.Fatalf("unexpected alias result: %+v", res.Papers)
		}
		if len(provider.opts.Sources) != 1 || provider.opts.Sources[0] != "semantic_scholar" {
			t.Fatalf("expected provider strategy to become source filter, got %+v", provider.opts.Sources)
		}
		if _, err := HandleToolSearch(context.Background(), reg, "wisdevSearchPapers", map[string]any{}); err == nil {
			t.Fatal("expected missing query error")
		}
	})

	t.Run("author lookup success after retry", func(t *testing.T) {
		reg := NewProviderRegistry()
		reg.Register(&testAuthorSearcher{name: "first", tools: []string{"author_lookup"}, err: io.EOF})
		reg.Register(&testAuthorSearcher{name: "second", tools: []string{"author_lookup"}, papers: []Paper{{ID: "a1"}}})

		res, err := HandleToolSearch(context.Background(), reg, "author_lookup", map[string]any{"authorId": "auth-1"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(res.Papers) != 1 || res.Papers[0].ID != "a1" {
			t.Fatalf("unexpected author lookup result: %+v", res.Papers)
		}
	})

	t.Run("paper lookup success", func(t *testing.T) {
		reg := NewProviderRegistry()
		reg.Register(&testPaperLookup{name: "paper", tools: []string{"paper_lookup"}, paper: &Paper{ID: "p1"}})

		res, err := HandleToolSearch(context.Background(), reg, "paper_lookup", map[string]any{"paperId": "paper-1"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(res.Papers) != 1 || res.Papers[0].ID != "p1" {
			t.Fatalf("unexpected paper lookup result: %+v", res.Papers)
		}
	})

	t.Run("missing params and unsupported tool", func(t *testing.T) {
		reg := NewProviderRegistry()
		if _, err := HandleToolSearch(context.Background(), reg, "author_lookup", map[string]any{}); err == nil {
			t.Fatal("expected missing authorId error")
		}
		if _, err := HandleToolSearch(context.Background(), reg, "paper_lookup", map[string]any{}); err == nil {
			t.Fatal("expected missing paperId error")
		}
		if _, err := HandleToolSearch(context.Background(), reg, "unknown", map[string]any{}); err == nil {
			t.Fatal("expected unsupported tool error")
		}
	})

	t.Run("no provider found after all candidates fail", func(t *testing.T) {
		reg := NewProviderRegistry()
		reg.Register(&testAuthorSearcher{name: "first", tools: []string{"author_lookup"}, err: io.EOF})
		reg.Register(&testPaperLookup{name: "paper", tools: []string{"paper_lookup"}, err: io.EOF})

		if _, err := HandleToolSearch(context.Background(), reg, "author_lookup", map[string]any{"authorId": "auth-1"}); err == nil {
			t.Fatal("expected no provider found error for author lookup")
		}
		if _, err := HandleToolSearch(context.Background(), reg, "paper_lookup", map[string]any{"paperId": "paper-1"}); err == nil {
			t.Fatal("expected no provider found error for paper lookup")
		}
	})
}

func TestRegistryGetCitationsAndSetRedis(t *testing.T) {
	reg := NewProviderRegistry()
	harness := newRedisHarness()
	client := newRedisClient(t, harness)
	reg.SetRedis(client)
	if reg.redis == nil {
		t.Fatal("expected redis client to be stored")
	}

	reg.Register(&testCitationProvider{name: "semantic_scholar", healthy: true, papers: []Paper{{ID: "c1"}}})
	res, err := reg.GetCitations(context.Background(), "paper-1", 3)
	if err != nil {
		t.Fatalf("unexpected citation error: %v", err)
	}
	if len(res) != 1 || res[0].ID != "c1" {
		t.Fatalf("unexpected citation result: %+v", res)
	}
	if reg.providers["semantic_scholar"].(*testCitationProvider).getCalled != 1 {
		t.Fatal("expected preferred citation provider to be used")
	}

	fallback := NewProviderRegistry()
	fallback.Register(&testCitationProvider{name: "alt", healthy: true, papers: []Paper{{ID: "c2"}}})
	res, err = fallback.GetCitations(context.Background(), "paper-2", 3)
	if err != nil {
		t.Fatalf("unexpected fallback citation error: %v", err)
	}
	if len(res) != 1 || res[0].ID != "c2" {
		t.Fatalf("unexpected fallback citation result: %+v", res)
	}

	none := NewProviderRegistry()
	none.Register(&testCitationProvider{name: "bad", healthy: false})
	if _, err := none.GetCitations(context.Background(), "paper-3", 3); err == nil {
		t.Fatal("expected citation lookup failure when no healthy provider exists")
	}
}

func TestGoogleScholarHelpersAndSearch(t *testing.T) {
	t.Setenv("SERPAPI_API_KEY", "test-key")
	provider := &GoogleScholarProvider{apiKey: "test-key"}

	if got := provider.Tools(); len(got) != 1 || got[0] != "author_lookup" {
		t.Fatalf("unexpected tools: %+v", got)
	}

	origTransport := SharedHTTPClient.Transport
	SharedHTTPClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(req.URL.String(), "engine=google_scholar_author"):
			body := `{"articles":[{"title":"Author paper","link":"https://example.com/a","authors":[{"name":"Ada Lovelace"}],"year":"2024","inline_links":{"cited_by":{"total":5}}}]}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		default:
			body := `{"organic_results":[{"title":"Scholar paper","link":"https://doi.org/10.1000/test","snippet":"This mentions DOI 10.1000/test.","year":"2023","publication_info":{"summary":"Conference 2023","authors":[{"name":"Ada Lovelace"}]},"inline_links":{"cited_by":{"total":12}}},{"snippet":"missing title"}]}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		}
	})
	t.Cleanup(func() { SharedHTTPClient.Transport = origTransport })

	papers, err := provider.Search(context.Background(), "quantum", SearchOpts{Limit: 2})
	if err != nil {
		t.Fatalf("unexpected google scholar search error: %v", err)
	}
	if len(papers) != 1 {
		t.Fatalf("expected one parsed paper, got %d", len(papers))
	}
	paper := papers[0]
	if paper.DOI != "10.1000/test" || paper.Year != 2023 || len(paper.Authors) != 1 || paper.CitationCount != 12 || paper.Venue != "Conference 2023" {
		t.Fatalf("unexpected parsed paper: %+v", paper)
	}
	if len(paper.SourceApis) != 1 || paper.SourceApis[0] != "google_scholar" {
		t.Fatalf("expected google scholar provenance, got %+v", paper.SourceApis)
	}

	authorPapers, err := provider.SearchByAuthor(context.Background(), "author-1", 1)
	if err != nil {
		t.Fatalf("unexpected author search error: %v", err)
	}
	if len(authorPapers) != 1 || authorPapers[0].CitationCount != 5 || len(authorPapers[0].SourceApis) != 1 || authorPapers[0].SourceApis[0] != "google_scholar" {
		t.Fatalf("unexpected author results: %+v", authorPapers)
	}

	parsedYear := parseYearFromString(" 1999 ")
	if parsedYear != 1999 {
		t.Fatalf("unexpected parsed year: %d", parsedYear)
	}
	if parseYearFromString("not-a-year") != 0 {
		t.Fatal("expected invalid year to return zero")
	}
	if asString(123) != "" || asString(" x ") != "x" {
		t.Fatal("unexpected asString behavior")
	}
	if got := parseDOIFromString("https://doi.org/10.1000/abc)."); got != "10.1000/abc" {
		t.Fatalf("unexpected DOI parse: %q", got)
	}
	authors := parseAuthorList([]any{"Ada", map[string]any{"name": "Grace"}, "", 1})
	if len(authors) != 2 || authors[0] != "Ada" || authors[1] != "Grace" {
		t.Fatalf("unexpected authors: %+v", authors)
	}
	if parseCitationCount(map[string]any{}) != 0 {
		t.Fatal("expected empty citation count to return zero")
	}
	if got := parseCitationCount(map[string]any{"inline_links": map[string]any{"cited_by": map[string]any{"total": json.Number("9")}}}); got != 9 {
		t.Fatalf("unexpected citation count: %d", got)
	}
	if got := sanitizePaperID(" Hello/World\\Next\n"); got != "hello_world_next" {
		t.Fatalf("unexpected paper id: %q", got)
	}

	t.Setenv("SERPAPI_API_KEY", "")
	emptyProvider := NewGoogleScholarProvider()
	if papers, err := emptyProvider.Search(context.Background(), "query", SearchOpts{}); err != nil || len(papers) != 0 {
		t.Fatalf("expected empty-key search to no-op, got %#v err=%v", papers, err)
	}
	if papers, err := emptyProvider.SearchByAuthor(context.Background(), "author", 1); err != nil || len(papers) != 0 {
		t.Fatalf("expected empty-key author search to no-op, got %#v err=%v", papers, err)
	}

	t.Run("Search fallback year and author parsing", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{"organic_results":[{"title":"Fallback Paper","link":"https://example.com/paper","snippet":"Abstract without DOI","publication_info":{"summary":"2022 Conference","authors":[{"name":"Ada"},{"name":"Grace"}]},"inline_links":{"cited_by":{"total":"nope"}}},{"title":"Raw Authors Paper","link":"https://example.com/raw","snippet":"Raw authors only","authors":[{"name":"Alan"},{"name":"Barbara"}]}]}`)
				return rec.Result(), nil
			}),
		}
		p := &GoogleScholarProvider{apiKey: "test-key"}
		papers, err := p.Search(context.Background(), "fallback", SearchOpts{Limit: 0})
		if err != nil {
			t.Fatalf("unexpected search error: %v", err)
		}
		if len(papers) != 2 {
			t.Fatalf("expected two parsed papers, got %+v", papers)
		}
		if papers[0].Year != 2022 || papers[0].ID != "google_scholar:fallback_paper" || len(papers[0].Authors) != 2 || papers[0].Venue != "2022 Conference" {
			t.Fatalf("unexpected fallback paper: %+v", papers[0])
		}
		if len(papers[0].SourceApis) != 1 || papers[0].SourceApis[0] != "google_scholar" {
			t.Fatalf("expected fallback source provenance, got %+v", papers[0].SourceApis)
		}
		if papers[1].ID != "google_scholar:raw_authors_paper" || len(papers[1].Authors) != 2 || len(papers[1].SourceApis) != 1 || papers[1].SourceApis[0] != "google_scholar" {
			t.Fatalf("unexpected raw authors paper: %+v", papers[1])
		}
	})
}

func TestGoogleScholarErrorBranches(t *testing.T) {
	t.Setenv("SERPAPI_API_KEY", "test-key")
	provider := &GoogleScholarProvider{apiKey: "test-key"}

	origTransport := SharedHTTPClient.Transport
	t.Cleanup(func() { SharedHTTPClient.Transport = origTransport })

	t.Run("Search transport status and decode errors", func(t *testing.T) {
		SharedHTTPClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch {
			case strings.Contains(req.URL.RawQuery, "q=status%3D503"):
				rec := httptest.NewRecorder()
				rec.WriteHeader(http.StatusServiceUnavailable)
				return rec.Result(), nil
			case strings.Contains(req.URL.RawQuery, "q=decode%3D1"):
				rec := httptest.NewRecorder()
				fmt.Fprint(rec, `invalid`)
				return rec.Result(), nil
			default:
				return nil, fmt.Errorf("transport failed")
			}
		})

		_, err := provider.Search(context.Background(), "status=503", SearchOpts{Limit: 1})
		if err == nil {
			t.Fatal("expected status error")
		}

		_, err = provider.Search(context.Background(), "transport", SearchOpts{Limit: 1})
		if err == nil {
			t.Fatal("expected transport error")
		}

		_, err = provider.Search(context.Background(), "decode=1", SearchOpts{Limit: 1})
		if err == nil {
			t.Fatal("expected decode error")
		}
	})

	t.Run("SearchByAuthor default limit and decode error", func(t *testing.T) {
		SharedHTTPClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if strings.Contains(req.URL.RawQuery, "num=20") {
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{"articles":[]}`)
				return rec.Result(), nil
			}
			return nil, fmt.Errorf("transport failed")
		})

		papers, err := provider.SearchByAuthor(context.Background(), "author-1", 0)
		if err != nil {
			t.Fatalf("unexpected default-limit error: %v", err)
		}
		if len(papers) != 0 {
			t.Fatalf("expected empty author results, got %+v", papers)
		}

		SharedHTTPClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
			rec := httptest.NewRecorder()
			fmt.Fprint(rec, `invalid`)
			return rec.Result(), nil
		})
		if _, err := provider.SearchByAuthor(context.Background(), "author-1", 1); err == nil {
			t.Fatal("expected decode error from author search")
		}
	})

	t.Run("SearchByAuthor skips blank titles", func(t *testing.T) {
		SharedHTTPClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
			rec := httptest.NewRecorder()
			rec.Header().Set("Content-Type", "application/json")
			fmt.Fprint(rec, `{"articles":[{"title":""},{"title":"Valid Author Paper","link":"https://example.com/paper"}]}`)
			return rec.Result(), nil
		})

		papers, err := provider.SearchByAuthor(context.Background(), "author-1", 1)
		if err != nil {
			t.Fatalf("unexpected blank-title author search error: %v", err)
		}
		if len(papers) != 1 || papers[0].Title != "Valid Author Paper" {
			t.Fatalf("unexpected filtered author results: %+v", papers)
		}
	})

	t.Run("Search build request error", func(t *testing.T) {
		origBuilder := buildGoogleScholarSearchEndpoint
		buildGoogleScholarSearchEndpoint = func(limit int, query, apiKey string) string {
			return "http://[::1"
		}
		t.Cleanup(func() { buildGoogleScholarSearchEndpoint = origBuilder })

		if _, err := provider.Search(context.Background(), "query", SearchOpts{Limit: 1}); err == nil {
			t.Fatal("expected search build request error")
		}
	})

	t.Run("SearchByAuthor build request error", func(t *testing.T) {
		origBuilder := buildGoogleScholarAuthorEndpoint
		buildGoogleScholarAuthorEndpoint = func(limit int, authorID, apiKey string) string {
			return "http://[::1"
		}
		t.Cleanup(func() { buildGoogleScholarAuthorEndpoint = origBuilder })

		if _, err := provider.SearchByAuthor(context.Background(), "author-1", 1); err == nil {
			t.Fatal("expected author build request error")
		}
	})

	t.Run("parseCitationCount handles numeric variants", func(t *testing.T) {
		if got := parseCitationCount(map[string]any{"inline_links": map[string]any{"cited_by": map[string]any{"total": float64(7)}}}); got != 7 {
			t.Fatalf("unexpected float citation count: %d", got)
		}
		if got := parseCitationCount(map[string]any{"inline_links": map[string]any{"cited_by": map[string]any{"total": int(8)}}}); got != 8 {
			t.Fatalf("unexpected int citation count: %d", got)
		}
		if got := parseCitationCount(map[string]any{"inline_links": map[string]any{"cited_by": map[string]any{"total": int64(9)}}}); got != 9 {
			t.Fatalf("unexpected int64 citation count: %d", got)
		}
		if got := parseCitationCount(map[string]any{"inline_links": map[string]any{"cited_by": map[string]any{"total": "nope"}}}); got != 0 {
			t.Fatalf("expected unsupported citation count type to return zero, got %d", got)
		}
		if got := parseCitationCount(map[string]any{"inline_links": map[string]any{}}); got != 0 {
			t.Fatalf("expected missing cited_by map to return zero, got %d", got)
		}
		if got := parseCitationCount(map[string]any{"inline_links": map[string]any{"cited_by": map[string]any{"total": json.Number("bad")}}}); got != 0 {
			t.Fatalf("expected invalid json.Number to return zero, got %d", got)
		}
	})
}

func TestPageIndexHelpers(t *testing.T) {
	t.Setenv("GO_PAGE_INDEX_RERANK_ENABLED", "true")
	if !ShouldRunPageIndexRerank(false) {
		t.Fatal("expected env flag to enable rerank")
	}
	if !ShouldRunPageIndexRerank(true) {
		t.Fatal("explicit request should always enable rerank")
	}
	t.Setenv("GO_PAGE_INDEX_RERANK_ENABLED", "false")
	if ShouldRunPageIndexRerank(false) {
		t.Fatal("disabled env and false request should not rerank")
	}

	if got := truncateForPrompt("abc", 5); got != "abc" {
		t.Fatalf("unexpected short truncation: %q", got)
	}
	if got := truncateForPrompt("abcdef", 3); got != "abc..." {
		t.Fatalf("unexpected long truncation: %q", got)
	}
	if got := clampFloat(-1, 0, 1); got != 0 {
		t.Fatalf("unexpected clamp low result: %v", got)
	}
	if got := clampFloat(2, 0, 1); got != 1 {
		t.Fatalf("unexpected clamp high result: %v", got)
	}

	prompt := buildPageIndexPrompt("quantum search", []Paper{{Title: strings.Repeat("a", 250), Abstract: strings.Repeat("b", 500), CitationCount: 3}}, 1)
	if !strings.Contains(prompt, "quantum search") || !strings.Contains(prompt, "index=0") {
		t.Fatalf("unexpected page index prompt: %s", prompt)
	}
	if strings.Contains(prompt, "Return strict JSON") {
		t.Fatalf("page index prompt should rely on schema-backed output, got %s", prompt)
	}

	t.Run("fetchGemini error branches", func(t *testing.T) {
		origGenerator := newPageIndexStructuredGenerator
		origMarshal := jsonMarshalFn
		t.Cleanup(func() {
			newPageIndexStructuredGenerator = origGenerator
			jsonMarshalFn = origMarshal
		})

		t.Setenv("AI_MODEL_RERANK_ID", "test-model")

		jsonMarshalFn = func(v any) ([]byte, error) {
			return nil, errors.New("marshal fail")
		}
		if rankings, ok := fetchGeminiPageIndexRankings(context.Background(), "query", []Paper{{Title: "a"}}, 1); ok || rankings != nil {
			t.Fatalf("expected marshal failure to short-circuit, got %#v ok=%v", rankings, ok)
		}

		jsonMarshalFn = origMarshal
		newPageIndexStructuredGenerator = func(context.Context) (pageIndexStructuredGenerator, error) {
			return nil, errors.New("generator init fail")
		}
		if rankings, ok := fetchGeminiPageIndexRankings(context.Background(), "query", []Paper{{Title: "a"}}, 1); ok || rankings != nil {
			t.Fatalf("expected generator init failure to short-circuit, got %#v ok=%v", rankings, ok)
		}

		newPageIndexStructuredGenerator = func(context.Context) (pageIndexStructuredGenerator, error) {
			return &stubPageIndexStructuredGenerator{text: ""}, nil
		}
		if rankings, ok := fetchGeminiPageIndexRankings(context.Background(), "query", []Paper{{Title: "a"}}, 1); ok || rankings != nil {
			t.Fatalf("expected empty-text failure to short-circuit, got %#v ok=%v", rankings, ok)
		}

		newPageIndexStructuredGenerator = func(context.Context) (pageIndexStructuredGenerator, error) {
			return &stubPageIndexStructuredGenerator{text: "not json"}, nil
		}
		if rankings, ok := fetchGeminiPageIndexRankings(context.Background(), "query", []Paper{{Title: "a"}}, 1); ok || rankings != nil {
			t.Fatalf("expected invalid ranking JSON to short-circuit, got %#v ok=%v", rankings, ok)
		}
	})

	origGenerator := newPageIndexStructuredGenerator
	t.Cleanup(func() { newPageIndexStructuredGenerator = origGenerator })
	stub := &stubPageIndexStructuredGenerator{text: `{"rankings":[{"index":1,"score":88.5,"reason":"best"},{"index":0,"score":91,"reason":"first"}]}`}
	newPageIndexStructuredGenerator = func(context.Context) (pageIndexStructuredGenerator, error) {
		return stub, nil
	}

	t.Setenv("AI_MODEL_RERANK_ID", "test-model")
	rankings, ok := fetchGeminiPageIndexRankings(context.Background(), "query", []Paper{{Title: "a"}, {Title: "b"}}, 2)
	if !ok {
		t.Fatal("expected rankings fetch to succeed with mocked structured runtime")
	}
	if len(rankings) != 2 || rankings[0].Index != 0 || rankings[1].Index != 1 {
		t.Fatalf("unexpected rankings: %+v", rankings)
	}
	if stub.model != "test-model" {
		t.Fatalf("expected reranker model to be forwarded, got %q", stub.model)
	}
	if !strings.Contains(stub.prompt, "schema") {
		t.Fatalf("expected prompt to mention schema-backed response, got %q", stub.prompt)
	}
	if !strings.Contains(stub.schema, "\"rankings\"") {
		t.Fatalf("expected rankings schema to be passed, got %q", stub.schema)
	}

	reordered := PageIndexRerankPapers(context.Background(), "query", []Paper{{Title: "a", Score: 1}, {Title: "b", Score: 2}}, 2)
	if len(reordered) != 2 || reordered[0].Score <= reordered[1].Score {
		t.Fatalf("unexpected reordered papers: %+v", reordered)
	}
}

func TestProviderRegistryHelpers(t *testing.T) {
	reg := NewProviderRegistry()
	reg.SetDefaultOrder([]string{"alpha", "beta"})
	reg.SetConcurrencyLimit("alpha", 3)
	reg.AdjustConcurrency("missing", nil)

	reg.Register(&mockProvider{name: "alpha", healthy: true})
	reg.Register(&mockProvider{name: "beta", healthy: false})
	ApplyDomainRoutes(reg)
	if got := reg.ProvidersFor("general"); len(got) != 1 || got[0].Name() != "alpha" {
		t.Fatalf("unexpected provider routing: %+v", got)
	}
	if all := reg.All(); len(all) != 2 {
		t.Fatalf("unexpected provider count: %d", len(all))
	}
}

type mockProvider struct {
	name    string
	healthy bool
	papers  []Paper
	query   string
	opts    SearchOpts
}

func (m *mockProvider) Name() string      { return m.name }
func (m *mockProvider) Domains() []string { return []string{"general"} }
func (m *mockProvider) Healthy() bool     { return m.healthy }
func (m *mockProvider) Tools() []string   { return nil }
func (m *mockProvider) Search(_ context.Context, query string, opts SearchOpts) ([]Paper, error) {
	m.query = query
	m.opts = opts
	return m.papers, nil
}

func TestProviderErrorFormatting(t *testing.T) {
	if got := providerError("provider", "value=%d", 7).Error(); got != "provider: value=7" {
		t.Fatalf("unexpected provider error: %q", got)
	}
}

func TestParallelSearchWithCacheBypass(t *testing.T) {
	reg := NewProviderRegistry()
	reg.Register(&mockProvider{name: "alpha", healthy: true})
	res := ParallelSearch(context.Background(), reg, "query", SearchOpts{SkipCache: true})
	if res.Providers["alpha"] != 0 {
		t.Fatalf("unexpected provider counts: %+v", res.Providers)
	}
}

func TestGenericFieldCoverageHelpers(t *testing.T) {
	if containsAny("alpha beta", "gamma", "beta") == false {
		t.Fatal("expected containsAny to match")
	}
	if containsAny("alpha beta", "gamma", "delta") {
		t.Fatal("expected containsAny to miss")
	}
	if got := paperKey(Paper{DOI: "10.1/abc"}); got != "doi:10.1/abc" {
		t.Fatalf("unexpected paper key: %q", got)
	}
	if got := paperKey(Paper{Title: "  Hello, World!  "}); got != "title:hello world" {
		t.Fatalf("unexpected title key: %q", got)
	}
	if got := normaliseTitle("Hello, World! 2024"); got != "hello world 2024" {
		t.Fatalf("unexpected normalised title: %q", got)
	}
	if merged := mergeProviderList([]string{"a", "b"}, []string{"b", "c"}, "c", "d"); len(merged) != 4 {
		t.Fatalf("unexpected merged providers: %+v", merged)
	}
	if got := getCacheKey("q", SearchOpts{}); got == "" {
		t.Fatal("expected cache key to be non-empty")
	}
}

func TestPageIndexFetchEarlyExit(t *testing.T) {
	origGenerator := newPageIndexStructuredGenerator
	t.Cleanup(func() { newPageIndexStructuredGenerator = origGenerator })
	newPageIndexStructuredGenerator = func(context.Context) (pageIndexStructuredGenerator, error) {
		return nil, fmt.Errorf("missing page index structured generator")
	}
	if rankings, ok := fetchGeminiPageIndexRankings(context.Background(), "query", []Paper{{Title: "a"}}, 1); ok || rankings != nil {
		t.Fatalf("expected generator initialization failure to short-circuit, got %#v ok=%v", rankings, ok)
	}
}
