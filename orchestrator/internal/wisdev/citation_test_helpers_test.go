package wisdev

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func startMockCitationResolveServer(t *testing.T) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/citations/resolve":
		default:
			http.NotFound(w, r)
			return
		}

		var req struct {
			Items []map[string]any `json:"items"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))

		resolved := make([]map[string]any, 0, len(req.Items))
		for idx, item := range req.Items {
			title := strings.TrimSpace(AsOptionalString(item["title"]))
			doi := strings.TrimSpace(AsOptionalString(item["doi"]))
			arxivID := firstNonEmpty(
				AsOptionalString(item["arxiv_id"]),
				AsOptionalString(item["arxivId"]),
				AsOptionalString(item["arxiv"]),
			)
			status := "verified"
			if doi == "" && arxivID == "" {
				status = "rejected"
			}
			sourceAuthority := "unknown"
			landingURL := ""
			if doi != "" {
				sourceAuthority = "crossref"
				landingURL = "https://doi.org/" + doi
			} else if arxivID != "" {
				sourceAuthority = "arxiv"
				landingURL = "https://arxiv.org/abs/" + arxivID
			}
			resolved = append(resolved, map[string]any{
				"id":                       firstNonEmpty(AsOptionalString(item["id"]), title),
				"doi":                      doi,
				"arxiv_id":                 arxivID,
				"title":                    title,
				"year":                     2024,
				"authors":                  []string{},
				"engine":                   "go-local",
				"api_confirmed":            doi != "" || arxivID != "",
				"author_fingerprints":      []string{"crossref", "semantic_scholar"},
				"verification_status":      status,
				"source_authority":         sourceAuthority,
				"resolver_agreement_count": 2,
				"resolver_conflict":        false,
				"conflict_note":            "",
				"landing_url":              landingURL,
				"semantic_scholar_id":      "ss:" + title,
				"openalex_id":              "oa:" + title,
				"provenance_hash":          "hash-" + title,
				"resolved":                 true,
				"verified":                 status == "verified",
			})
			if status != "verified" {
				resolved[idx]["resolver_agreement_count"] = 1
			}
		}

		blockingIssues := []string{}
		promotionEligible := true
		for _, item := range resolved {
			if AsOptionalString(item["verification_status"]) != "verified" {
				promotionEligible = false
				blockingIssues = append(blockingIssues, "rejected:"+AsOptionalString(item["id"]))
			}
		}

		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"data": map[string]any{
				"resolved":          resolved,
				"resolverTrace":     []map[string]any{},
				"promotionEligible": promotionEligible,
				"blockingIssues":    blockingIssues,
				"engine":            "go-local",
			},
		}))
	}))
}
