package wisdev

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

type rankedTool struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Risk        string  `json:"risk"`
	Score       float64 `json:"score"`
}

type rustToolRankInput struct {
	Query string       `json:"query"`
	Tools []rankedTool `json:"tools"`
	Limit int          `json:"limit"`
}

type rustToolRankOutput struct {
	Ranked []rankedTool `json:"ranked"`
}

type citationFormatItemAuthor struct {
	Family string `json:"family,omitempty"`
	Given  string `json:"given,omitempty"`
}

type citationFormatItemIssued struct {
	DateParts [][]int `json:"date-parts,omitempty"`
}

type citationFormatItem struct {
	ID             string                     `json:"id"`
	Type           string                     `json:"type,omitempty"`
	Title          string                     `json:"title"`
	Author         []citationFormatItemAuthor `json:"author,omitempty"`
	Issued         *citationFormatItemIssued  `json:"issued,omitempty"`
	ContainerTitle string                     `json:"container-title,omitempty"`
	Volume         string                     `json:"volume,omitempty"`
	Issue          string                     `json:"issue,omitempty"`
	Page           string                     `json:"page,omitempty"`
	DOI            string                     `json:"DOI,omitempty"`
	URL            string                     `json:"url,omitempty"`
}

type citationFormatRequest struct {
	Style  string               `json:"style"`
	Locale string               `json:"locale,omitempty"`
	Output string               `json:"output,omitempty"`
	Items  []citationFormatItem `json:"items"`
}

type citationFormatResultItem struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type citationFormatResponse struct {
	OK        bool                       `json:"ok"`
	Style     string                     `json:"style"`
	Locale    string                     `json:"locale"`
	Output    string                     `json:"output"`
	Formatted []citationFormatResultItem `json:"formatted"`
	Engine    string                     `json:"engine"`
}

type rustBridgeEnvelope struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error string          `json:"error"`
}

var bridgeClient = &http.Client{
	Timeout: 5 * time.Second,
}

func rustBridgeBaseURL() string {
	base := strings.TrimSpace(os.Getenv("RUST_GATEWAY_INTERNAL_URL"))
	if base == "" {
		return "http://localhost:8080/internal/wisdev-bridge"
	}
	return strings.TrimRight(base, "/") + "/internal/wisdev-bridge"
}

func runRustBridgeMain(command string, payload any, out any) error {
	url := fmt.Sprintf("%s/%s", rustBridgeBaseURL(), command)
	
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	
	if key := os.Getenv("INTERNAL_SERVICE_KEY"); key != "" {
		req.Header.Set("X-Internal-Service-Key", key)
	}

	resp, err := bridgeClient.Do(req)
	if err != nil {
		return fmt.Errorf("rust bridge request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("rust bridge returned status %d", resp.StatusCode)
	}

	return decodeRustBridgeOutput(resp.Body, out)
}

func decodeRustBridgeOutput(r io.Reader, out any) error {
	var envelope rustBridgeEnvelope
	if err := json.NewDecoder(r).Decode(&envelope); err != nil {
		return fmt.Errorf("failed to decode rust bridge response: %w", err)
	}

	if !envelope.OK {
		if strings.TrimSpace(envelope.Error) != "" {
			return errors.New(strings.TrimSpace(envelope.Error))
		}
		return errors.New("rust bridge reported failure")
	}

	if out == nil {
		return nil
	}

	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return nil
	}

	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return fmt.Errorf("decode rust bridge envelope data: %w", err)
	}
	return nil
}

func RankTools(query string, tools []ToolDefinition, limit int) []rankedTool {
	if limit <= 0 {
		limit = 5
	}

	candidates := make([]rankedTool, 0, len(tools))
	for _, Tool := range tools {
		candidates = append(candidates, rankedTool{
			Name:        Tool.Name,
			Description: Tool.Description,
			Risk:        string(Tool.Risk),
		})
	}

	var rustOut rustToolRankOutput
	if err := runRustBridgeMain("retrieval-rank", rustToolRankInput{Query: query, Tools: candidates, Limit: limit}, &rustOut); err == nil && len(rustOut.Ranked) > 0 {
		if len(rustOut.Ranked) > limit {
			return rustOut.Ranked[:limit]
		}
		return rustOut.Ranked
	}

	queryTerms := splitTerms(query)
	for i := range candidates {
		hay := strings.ToLower(candidates[i].Name + " " + candidates[i].Description)
		score := 0.0
		for _, term := range queryTerms {
			if strings.Contains(hay, term) {
				score += 1.0
			}
		}
		switch candidates[i].Risk {
		case "low":
			score += 0.2
		case "medium":
			score += 0.1
		}
		candidates[i].Score = score
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score == candidates[j].Score {
			return candidates[i].Name < candidates[j].Name
		}
		return candidates[i].Score > candidates[j].Score
	})

	if len(candidates) > limit {
		return candidates[:limit]
	}
	return candidates
}

func splitTerms(text string) []string {
	raw := strings.Fields(strings.ToLower(text))
	out := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		term := strings.TrimSpace(strings.Trim(item, ",.;:!?()[]{}\"'"))
		if len(term) < 2 {
			continue
		}
		if _, ok := seen[term]; ok {
			continue
		}
		seen[term] = struct{}{}
		out = append(out, term)
	}
	return out
}

func ComputeTraceIntegrityHash(payload any) string {
	body, err := json.Marshal(payload)
	if err != nil {
		return ""
	}

	var out struct {
		Hash string `json:"hash"`
	}
	if err := runRustBridgeMain("integrity-hash", map[string]string{"payload": string(body)}, &out); err == nil && strings.TrimSpace(out.Hash) != "" {
		return out.Hash
	}

	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

func authorLabelForStyle(style string, author citationFormatItemAuthor) string {
	family := strings.TrimSpace(author.Family)
	given := strings.TrimSpace(author.Given)
	initial := ""
	if given != "" {
		r := []rune(given)
		if len(r) > 0 {
			initial = string(r[0]) + "."
		}
	}
	if style == "mla" {
		switch {
		case family == "":
			return given
		case given == "":
			return family
		default:
			return family + ", " + given
		}
	}
	switch {
	case family == "":
		return given
	case initial == "":
		return family
	default:
		return family + ", " + initial
	}
}

func formatAuthorsForStyle(style string, authors []citationFormatItemAuthor) string {
	if len(authors) == 0 {
		return "Unknown"
	}
	formatted := make([]string, 0, len(authors))
	for _, author := range authors {
		formatted = append(formatted, authorLabelForStyle(style, author))
	}
	if len(formatted) == 1 {
		return formatted[0]
	}
	if len(formatted) == 2 {
		return formatted[0] + " & " + formatted[1]
	}
	return formatted[0] + ", et al."
}

func localFormatCitation(style string, item citationFormatItem) (string, error) {
	style = strings.ToLower(strings.TrimSpace(style))
	id := strings.TrimSpace(item.ID)
	if id == "" {
		return "", errors.New("invalid_item_payload:missing_id")
	}
	title := strings.TrimSpace(item.Title)
	if title == "" {
		return "", fmt.Errorf("invalid_item_payload:missing_title:%s", id)
	}
	year := 0
	if item.Issued != nil && len(item.Issued.DateParts) > 0 && len(item.Issued.DateParts[0]) > 0 {
		year = item.Issued.DateParts[0][0]
	}
	author := formatAuthorsForStyle(style, item.Author)
	journal := strings.TrimSpace(item.ContainerTitle)
	volume := strings.TrimSpace(item.Volume)
	issue := strings.TrimSpace(item.Issue)
	page := strings.TrimSpace(item.Page)
	doi := strings.TrimSpace(item.DOI)
	url := strings.TrimSpace(item.URL)

	switch style {
	case "apa":
		out := fmt.Sprintf("%s (%d). %s.", author, year, title)
		if journal != "" {
			out += " " + journal
			if volume != "" {
				out += ", " + volume
			}
			if issue != "" {
				out += "(" + issue + ")"
			}
			if page != "" {
				out += ", " + page
			}
			out += "."
		}
		if doi != "" {
			out += " https://doi.org/" + doi
		} else if url != "" {
			out += " " + url
		}
		return out, nil
	case "mla":
		out := fmt.Sprintf("%s. \"%s.\"", author, title)
		if journal != "" {
			out += " " + journal
		}
		if volume != "" {
			out += ", vol. " + volume
		}
		if issue != "" {
			out += ", no. " + issue
		}
		if year > 0 {
			out += fmt.Sprintf(", %d", year)
		}
		if page != "" {
			out += ", pp. " + page
		}
		out += "."
		if doi != "" {
			out += " https://doi.org/" + doi
		} else if url != "" {
			out += " " + url
		}
		return out, nil
	case "chicago":
		out := fmt.Sprintf("%s. \"%s.\"", author, title)
		if journal != "" {
			out += " " + journal
		}
		if volume != "" {
			out += " " + volume
		}
		if issue != "" {
			out += ", no. " + issue
		}
		if year > 0 {
			out += fmt.Sprintf(" (%d)", year)
		}
		if page != "" {
			out += ": " + page
		}
		out += "."
		if doi != "" {
			out += " https://doi.org/" + doi + "."
		} else if url != "" {
			out += " " + url + "."
		}
		return out, nil
	default:
		return "", fmt.Errorf("unsupported_style:%s", style)
	}
}

func formatCitations(req citationFormatRequest) (citationFormatResponse, error) {
	style := strings.ToLower(strings.TrimSpace(req.Style))
	if style == "" {
		style = "apa"
	}
	if req.Locale == "" {
		req.Locale = "en-US"
	}
	if req.Output == "" {
		req.Output = "bibliography"
	}
	if len(req.Items) == 0 {
		return citationFormatResponse{}, errors.New("invalid_item_payload:empty_items")
	}

	var out citationFormatResponse
	if err := runRustBridgeMain("citation-format", req, &out); err == nil && out.OK && len(out.Formatted) > 0 {
		return out, nil
	}

	if style != "apa" && style != "mla" && style != "chicago" {
		return citationFormatResponse{}, fmt.Errorf("unsupported_style:%s", style)
	}

	formatted := make([]citationFormatResultItem, 0, len(req.Items))
	for _, item := range req.Items {
		text, err := localFormatCitation(style, item)
		if err != nil {
			return citationFormatResponse{}, err
		}
		formatted = append(formatted, citationFormatResultItem{
			ID:   item.ID,
			Text: text,
		})
	}
	return citationFormatResponse{
		OK:        true,
		Style:     style,
		Locale:    req.Locale,
		Output:    req.Output,
		Formatted: formatted,
		Engine:    "go-fallback",
	}, nil
}
