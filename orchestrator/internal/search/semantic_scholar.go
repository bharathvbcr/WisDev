package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/resilience"
	"strings"
	"time"
)

type SemanticScholarProvider struct {
	BaseProvider
	baseURL string
}

var _ SearchProvider = (*SemanticScholarProvider)(nil)

var newRequestWithContext = http.NewRequestWithContext

const semanticScholarPaperFields = "title,abstract,url,externalIds,authors,year,citationCount,influentialCitationCount,referenceCount,venue,openAccessPdf"
const semanticScholarCitationFields = "citingPaper.title,citingPaper.abstract,citingPaper.url,citingPaper.externalIds,citingPaper.authors,citingPaper.year,citingPaper.citationCount,citingPaper.influentialCitationCount,citingPaper.referenceCount,citingPaper.venue,citingPaper.openAccessPdf"
const semanticScholarReferenceFields = "citedPaper.title,citedPaper.abstract,citedPaper.url,citedPaper.externalIds,citedPaper.authors,citedPaper.year,citedPaper.citationCount,citedPaper.influentialCitationCount,citedPaper.referenceCount,citedPaper.venue,citedPaper.openAccessPdf"

// Ensure Semantic Scholar satisfies the citation-graph contract (forward + backward).
var _ CitationGraphProvider = (*SemanticScholarProvider)(nil)

func NewSemanticScholarProvider() *SemanticScholarProvider {
	return &SemanticScholarProvider{
		baseURL: "https://api.semanticscholar.org/graph/v1/paper/search",
	}
}

func (s *SemanticScholarProvider) Name() string { return "semantic_scholar" }

func (s *SemanticScholarProvider) Tools() []string {
	return []string{"author_lookup", "paper_lookup"}
}

func (s *SemanticScholarProvider) Domains() []string {
	return []string{"medicine", "cs", "ai", "social", "physics", "engineering", "humanities", "biology", "neuro"}
}

func semanticScholarAPIKey(ctx context.Context) string {
	apiKey, err := resilience.GetSecret(ctx, "SEMANTIC_SCHOLAR_API_KEY")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(apiKey)
}

// ... (existing S2Paper, S2Response, and Search method)

func (s *SemanticScholarProvider) SearchByAuthor(ctx context.Context, authorID string, limit int) ([]Paper, error) {
	if limit <= 0 {
		limit = 20
	}
	// authorID can be S2 Author ID or name (though ID is better)
	reqUrl := fmt.Sprintf("https://api.semanticscholar.org/graph/v1/author/%s/papers?limit=%d&fields=%s", url.PathEscape(authorID), limit, semanticScholarPaperFields)
	return s.fetchS2Papers(ctx, reqUrl)
}

func (s *SemanticScholarProvider) SearchByPaperID(ctx context.Context, paperID string) (*Paper, error) {
	// paperID can be S2 ID, DOI, arXiv ID, etc.
	id := strings.TrimPrefix(paperID, "s2:")
	reqUrl := fmt.Sprintf("https://api.semanticscholar.org/graph/v1/paper/%s?fields=%s", url.PathEscape(id), semanticScholarPaperFields)

	req, err := newRequestWithContext(ctx, http.MethodGet, reqUrl, nil)
	if err != nil {
		return nil, err
	}

	apiKey := semanticScholarAPIKey(ctx)
	if apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
	}

	resp, err := SharedHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if providerHTTPErrorKind(resp) == "rate_limit" {
			return nil, fmt.Errorf("S2 lookup failed: rate limit exceeded (%d)", resp.StatusCode)
		}
		if providerHTTPErrorKind(resp) == "upstream_5xx" {
			return nil, fmt.Errorf("S2 lookup failed: upstream error (%d)", resp.StatusCode)
		}
		return nil, fmt.Errorf("S2 lookup failed: %d", resp.StatusCode)
	}

	var p S2Paper
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return nil, fmt.Errorf("S2 lookup failed to parse response: %w", err)
	}

	paper := mapSemanticScholarPaper(p)
	return &paper, nil
}

func (s *SemanticScholarProvider) GetCitations(ctx context.Context, paperID string, limit int) ([]Paper, error) {
	if limit <= 0 {
		limit = 20
	}
	id := strings.TrimPrefix(paperID, "s2:")
	// We want the papers that CITED this paper (citingPaper fields)
	reqUrl := fmt.Sprintf("https://api.semanticscholar.org/graph/v1/paper/%s/citations?limit=%d&fields=%s", url.PathEscape(id), limit, semanticScholarCitationFields)

	req, err := newRequestWithContext(ctx, http.MethodGet, reqUrl, nil)
	if err != nil {
		return nil, err
	}

	apiKey := semanticScholarAPIKey(ctx)
	if apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
	}

	resp, err := SharedHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if providerHTTPErrorKind(resp) == "rate_limit" {
			return nil, fmt.Errorf("S2 citations failed: rate limit exceeded (%d)", resp.StatusCode)
		}
		if providerHTTPErrorKind(resp) == "upstream_5xx" {
			return nil, fmt.Errorf("S2 citations failed: upstream error (%d)", resp.StatusCode)
		}
		return nil, fmt.Errorf("S2 citations failed: %d", resp.StatusCode)
	}

	var result struct {
		Data []struct {
			CitingPaper S2Paper `json:"citingPaper"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("S2 citations failed to parse response: %w", err)
	}

	var papers []Paper
	for _, entry := range result.Data {
		papers = append(papers, mapSemanticScholarPaper(entry.CitingPaper))
	}
	return papers, nil
}

func (s *SemanticScholarProvider) GetReferences(ctx context.Context, paperID string, limit int) ([]Paper, error) {
	if limit <= 0 {
		limit = 20
	}
	id := strings.TrimPrefix(paperID, "s2:")
	// Papers that this paper cites (citedPaper fields)
	reqUrl := fmt.Sprintf("https://api.semanticscholar.org/graph/v1/paper/%s/references?limit=%d&fields=%s", url.PathEscape(id), limit, semanticScholarReferenceFields)

	req, err := newRequestWithContext(ctx, http.MethodGet, reqUrl, nil)
	if err != nil {
		return nil, err
	}

	apiKey := semanticScholarAPIKey(ctx)
	if apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
	}

	resp, err := SharedHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if providerHTTPErrorKind(resp) == "rate_limit" {
			return nil, fmt.Errorf("S2 references failed: rate limit exceeded (%d)", resp.StatusCode)
		}
		if providerHTTPErrorKind(resp) == "upstream_5xx" {
			return nil, fmt.Errorf("S2 references failed: upstream error (%d)", resp.StatusCode)
		}
		return nil, fmt.Errorf("S2 references failed: %d", resp.StatusCode)
	}

	var result struct {
		Data []struct {
			CitedPaper S2Paper `json:"citedPaper"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("S2 references failed to parse response: %w", err)
	}

	var papers []Paper
	for _, entry := range result.Data {
		if strings.TrimSpace(entry.CitedPaper.Title) == "" && strings.TrimSpace(entry.CitedPaper.PaperID) == "" {
			continue
		}
		papers = append(papers, mapSemanticScholarPaper(entry.CitedPaper))
	}
	return papers, nil
}

func (s *SemanticScholarProvider) fetchS2Papers(ctx context.Context, reqUrl string) ([]Paper, error) {
	req, err := newRequestWithContext(ctx, http.MethodGet, reqUrl, nil)
	if err != nil {
		return nil, err
	}

	apiKey := semanticScholarAPIKey(ctx)
	if apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
	}

	resp, err := SharedHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if providerHTTPErrorKind(resp) == "rate_limit" {
			return nil, fmt.Errorf("S2 request failed: rate limit exceeded (%d)", resp.StatusCode)
		}
		if providerHTTPErrorKind(resp) == "upstream_5xx" {
			return nil, fmt.Errorf("S2 request failed: upstream error (%d)", resp.StatusCode)
		}
		return nil, fmt.Errorf("S2 request failed: %d", resp.StatusCode)
	}

	var s2Res S2Response
	if err := json.NewDecoder(resp.Body).Decode(&s2Res); err != nil {
		return nil, fmt.Errorf("S2 request failed to parse response: %w", err)
	}

	var papers []Paper
	for _, p := range s2Res.Data {
		papers = append(papers, mapSemanticScholarPaper(p))
	}
	return papers, nil
}

type S2Paper struct {
	PaperID     string `json:"paperId"`
	Title       string `json:"title"`
	Abstract    string `json:"abstract"`
	URL         string `json:"url"`
	ExternalIds struct {
		DOI string `json:"DOI"`
	} `json:"externalIds"`
	Authors []struct {
		Name string `json:"name"`
	} `json:"authors"`
	Year                     int    `json:"year"`
	CitationCount            int    `json:"citationCount"`
	InfluentialCitationCount int    `json:"influentialCitationCount"`
	ReferenceCount           int    `json:"referenceCount"`
	Venue                    string `json:"venue"`
	OpenAccessPdf            *struct {
		URL string `json:"url"`
	} `json:"openAccessPdf"`
}

type S2Response struct {
	Data []S2Paper `json:"data"`
}

func (s *SemanticScholarProvider) Search(ctx context.Context, query string, opts SearchOpts) ([]Paper, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}

	providerQuery := buildSemanticScholarSearchQuery(query)
	if providerQuery == "" {
		providerQuery = strings.TrimSpace(query)
	}
	if providerQuery != strings.TrimSpace(query) {
		logProviderSearchStage(ctx, "provider_query_normalized", s.Name(), query, opts,
			"result", "normalized",
			"provider_query_preview", queryPreview(providerQuery),
		)
	}

	reqUrl := fmt.Sprintf("%s?query=%s&limit=%d&fields=%s", s.baseURL, url.QueryEscape(providerQuery), limit, semanticScholarPaperFields)

	if opts.YearFrom > 0 {
		if opts.YearTo > 0 {
			reqUrl += fmt.Sprintf("&year=%d-%d", opts.YearFrom, opts.YearTo)
		} else {
			reqUrl += fmt.Sprintf("&year=%d-", opts.YearFrom)
		}
	}

	req, err := newRequestWithContext(ctx, http.MethodGet, reqUrl, nil)
	if err != nil {
		s.RecordFailure()
		return nil, providerError("semantic_scholar", "build request: %v", err)
	}

	apiKey := semanticScholarAPIKey(ctx)
	if apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
	}

	resp, err := SharedHTTPClient.Do(req)
	if err != nil {
		s.RecordFailure()
		return nil, providerError("semantic_scholar", "request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		s.RecordFailure()
		return nil, providerHTTPStatusError("semantic_scholar", resp)
	}

	var s2Res S2Response
	if err := json.NewDecoder(resp.Body).Decode(&s2Res); err != nil {
		s.RecordFailure()
		return nil, providerError("semantic_scholar", "failed to parse response: %v", err)
	}

	var papers []Paper
	for _, p := range s2Res.Data {
		if !isRelevantSemanticScholarPaper(query, p) {
			continue
		}
		papers = append(papers, mapSemanticScholarPaper(p))
	}
	logSemanticScholarFilterApplied(ctx, query, opts, len(s2Res.Data), len(papers))

	s.RecordSuccess()
	return papers, nil
}

func buildSemanticScholarSearchQuery(query string) string {
	terms := significantSemanticScholarQueryTerms(query)
	if len(terms) < 2 {
		return strings.TrimSpace(query)
	}
	return strings.Join(terms, " ")
}

func isRelevantSemanticScholarPaper(query string, paper S2Paper) bool {
	return isRelevantProviderSearchText(query, paper.Title, paper.Abstract, paper.Venue)
}

func isRelevantProviderSearchText(query string, title string, abstract string, venue string) bool {
	if strings.TrimSpace(title) == "" {
		return false
	}

	terms := significantSemanticScholarQueryTerms(query)
	if len(terms) < 2 {
		return true
	}

	titleTokens := crossrefTextTokens(title + " " + venue)
	textTokens := crossrefTextTokens(title + " " + abstract + " " + venue)
	if len(textTokens) == 0 {
		return false
	}
	if !providerPassesFocusedTunnelingChipEvidence(terms, titleTokens, textTokens) {
		return false
	}

	matches := 0
	anchorTerms := 0
	anchorMatched := false
	seen := make(map[string]struct{}, len(terms))
	for _, term := range terms {
		if _, exists := seen[term]; exists {
			continue
		}
		seen[term] = struct{}{}
		isAnchorTerm := anchorTerms < 2
		anchorTerms++
		if matched := crossrefTermMatches(term, textTokens); matched {
			matches++
			if isAnchorTerm {
				anchorMatched = true
			}
		}
	}

	if len(seen) >= 3 && !anchorMatched {
		return false
	}
	required := 2
	if len(seen) >= 5 {
		required = 3
	}
	return matches >= required
}

var semanticScholarTunnelContextTerms = map[string]struct{}{
	"barrier": {}, "current": {}, "diode": {}, "effect": {}, "field": {}, "gate": {},
	"junction": {}, "leakage": {}, "oxide": {}, "transistor": {},
}

var semanticScholarNanoscaleChipTerms = map[string]struct{}{
	"1nm": {}, "2nm": {}, "3nm": {}, "4nm": {}, "5nm": {}, "chip": {}, "cmos": {},
	"finfet": {}, "gate": {}, "nanogap": {}, "nanometer": {}, "nanopore": {},
	"nanoscale": {}, "nm": {}, "oxide": {}, "semiconductor": {}, "soi": {},
	"transistor": {},
}

var semanticScholarSemiconductorChipTerms = map[string]struct{}{
	"channel": {}, "cmos": {}, "dielectric": {}, "fd": {}, "fet": {}, "finfet": {},
	"gaa": {}, "gate": {}, "hardware": {}, "leakage": {}, "mos": {}, "mosfet": {},
	"nanoelectronic": {}, "nmos": {}, "oxide": {}, "pmos": {}, "puf": {},
	"semiconductor": {}, "silicon": {}, "soi": {}, "transistor": {}, "diode": {},
}

var semanticScholarSemiconductorChipTitleTerms = map[string]struct{}{
	"channel": {}, "cmos": {}, "device": {}, "dielectric": {}, "diode": {}, "fd": {},
	"fet": {}, "finfet": {}, "gaa": {}, "gate": {}, "hardware": {}, "leakage": {},
	"mos": {}, "mosfet": {}, "nanoelectronic": {}, "nmos": {}, "oxide": {},
	"pmos": {}, "puf": {}, "semiconductor": {}, "silicon": {}, "soi": {},
	"transistor": {},
}

var semanticScholarChipHardwareContextTerms = map[string]struct{}{
	"fingerprint": {}, "hardware": {}, "leakage": {}, "puf": {}, "security": {},
}

var semanticScholarQueryStopWords = map[string]struct{}{
	"article": {}, "articles": {}, "find": {}, "give": {}, "paper": {}, "papers": {},
	"please": {}, "publication": {}, "publications": {}, "research": {}, "show": {},
	"study": {}, "studies": {}, "top": {},
}

func significantSemanticScholarQueryTerms(query string) []string {
	tokens := crossrefTextTokens(query)
	terms := make([]string, 0, len(tokens))
	seen := make(map[string]struct{}, len(tokens))
	for _, token := range tokens {
		if semanticScholarStopWord(token) {
			continue
		}
		token = normalizeCrossrefToken(token)
		if len(token) < 2 {
			continue
		}
		if semanticScholarStopWord(token) {
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		terms = append(terms, token)
	}
	return terms
}

func semanticScholarStopWord(token string) bool {
	if _, stop := crossrefQueryStopWords[token]; stop {
		return true
	}
	_, stop := semanticScholarQueryStopWords[token]
	return stop
}

func semanticScholarHasTunnelingEvidence(tokens []string) bool {
	for i, token := range tokens {
		normalized := normalizeCrossrefToken(token)
		if normalized == "tunneling" {
			return true
		}
		if normalized != "tunnel" {
			continue
		}
		if semanticScholarNeighborIn(tokens, i, semanticScholarTunnelContextTerms) {
			return true
		}
	}
	return false
}

func semanticScholarHasNanoscaleChipIntent(terms []string) bool {
	return crossrefHasNanoscaleTerm(terms) || crossrefHasAnyTerm(terms, "chip", "gate", "oxide", "transistor", "cmos", "finfet", "nanoelectronic")
}

func providerPassesFocusedTunnelingChipEvidence(terms []string, titleTokens []string, textTokens []string) bool {
	hasTunnelingIntent := crossrefHasTerm(terms, "tunneling")
	hasNanoscaleChipIntent := semanticScholarHasNanoscaleChipIntent(terms)
	if hasTunnelingIntent && !semanticScholarHasTunnelingEvidence(textTokens) {
		return false
	}
	if hasNanoscaleChipIntent && !semanticScholarHasNanoscaleChipEvidence(textTokens) {
		return false
	}
	if hasTunnelingIntent && hasNanoscaleChipIntent {
		if !semanticScholarHasSemiconductorChipEvidence(textTokens) {
			return false
		}
		if !semanticScholarHasSemiconductorChipTitleEvidence(titleTokens) {
			return false
		}
	}
	return true
}

func semanticScholarHasNanoscaleChipEvidence(tokens []string) bool {
	for i, token := range tokens {
		normalized := normalizeCrossrefToken(token)
		if _, ok := semanticScholarNanoscaleChipTerms[normalized]; ok {
			return true
		}
		if strings.HasSuffix(normalized, "nm") {
			return true
		}
		if normalized == "sub" && semanticScholarNeighborIn(tokens, i, semanticScholarNanoscaleChipTerms) {
			return true
		}
	}
	return false
}

func semanticScholarHasSemiconductorChipEvidence(tokens []string) bool {
	for i, token := range tokens {
		normalized := normalizeCrossrefToken(token)
		if _, ok := semanticScholarSemiconductorChipTerms[normalized]; ok {
			return true
		}
		if normalized == "chip" && semanticScholarNeighborInWindow(tokens, i, semanticScholarChipHardwareContextTerms, 3) {
			return true
		}
	}
	return false
}

func semanticScholarHasSemiconductorChipTitleEvidence(tokens []string) bool {
	for i, token := range tokens {
		normalized := normalizeCrossrefToken(token)
		if _, ok := semanticScholarSemiconductorChipTitleTerms[normalized]; ok {
			return true
		}
		if normalized == "chip" && semanticScholarNeighborInWindow(tokens, i, semanticScholarChipHardwareContextTerms, 3) {
			return true
		}
	}
	return false
}

func semanticScholarNeighborIn(tokens []string, index int, terms map[string]struct{}) bool {
	for _, neighborIndex := range []int{index - 1, index + 1} {
		if neighborIndex < 0 || neighborIndex >= len(tokens) {
			continue
		}
		neighbor := normalizeCrossrefToken(tokens[neighborIndex])
		if _, ok := terms[neighbor]; ok {
			return true
		}
	}
	return false
}

func semanticScholarNeighborInWindow(tokens []string, index int, terms map[string]struct{}, width int) bool {
	if width < 1 {
		return false
	}
	for neighborIndex := index - width; neighborIndex <= index+width; neighborIndex++ {
		if neighborIndex < 0 || neighborIndex >= len(tokens) || neighborIndex == index {
			continue
		}
		neighbor := normalizeCrossrefToken(tokens[neighborIndex])
		if _, ok := terms[neighbor]; ok {
			return true
		}
	}
	return false
}

func logSemanticScholarFilterApplied(ctx context.Context, query string, opts SearchOpts, rawCount int, keptCount int) {
	if rawCount <= keptCount {
		return
	}
	logProviderSearchStage(ctx, "provider_result_filter_applied", "semantic_scholar", query, opts,
		"result", "filtered",
		"raw_result_count", rawCount,
		"kept_count", keptCount,
		"filtered_count", rawCount-keptCount,
	)
}

func mapSemanticScholarPaper(p S2Paper) Paper {
	authors := make([]string, 0, len(p.Authors))
	for _, a := range p.Authors {
		authors = append(authors, strings.TrimSpace(a.Name))
	}

	oaUrl := ""
	if p.OpenAccessPdf != nil {
		oaUrl = p.OpenAccessPdf.URL
	}

	return Paper{
		ID:                       "s2:" + p.PaperID,
		Title:                    p.Title,
		Abstract:                 p.Abstract,
		Link:                     p.URL,
		DOI:                      p.ExternalIds.DOI,
		Source:                   "semantic_scholar",
		SourceApis:               []string{"semantic_scholar"},
		Authors:                  authors,
		Year:                     p.Year,
		Venue:                    p.Venue,
		CitationCount:            p.CitationCount,
		InfluentialCitationCount: p.InfluentialCitationCount,
		ReferenceCount:           p.ReferenceCount,
		OpenAccessUrl:            oaUrl,
		PdfUrl:                   oaUrl,
	}
}
