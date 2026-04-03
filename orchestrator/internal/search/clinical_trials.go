package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ClinicalTrialsProvider searches ClinicalTrials.gov via their v2 REST API.
// No API key required. Returns clinical study records.
// Best for medicine, pharmacology, and health research.
type ClinicalTrialsProvider struct {
	BaseProvider
	baseURL string
}

var _ SearchProvider = (*ClinicalTrialsProvider)(nil)

func NewClinicalTrialsProvider() *ClinicalTrialsProvider {
	return &ClinicalTrialsProvider{baseURL: "https://clinicaltrials.gov/api/v2/studies"}
}

func (c *ClinicalTrialsProvider) Name() string { return "clinical_trials" }
func (c *ClinicalTrialsProvider) Domains() []string {
	return []string{"medicine"}
}

func (c *ClinicalTrialsProvider) Search(ctx context.Context, query string, opts SearchOpts) ([]Paper, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}

	params := url.Values{}
	params.Set("query.term", query)
	params.Set("pageSize", fmt.Sprintf("%d", limit))
	params.Set("fields", "NCTId,BriefTitle,BriefSummary,Condition,Phase,OverallStatus,StartDate,CompletionDate,LeadSponsorName")
	params.Set("format", "json")

	reqURL := c.baseURL + "?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		c.RecordFailure()
		return nil, providerError("clinical_trials", "build request: %v", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := SharedHTTPClient.Do(req)
	if err != nil {
		c.RecordFailure()
		return nil, providerError("clinical_trials", "request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		c.RecordFailure()
		return nil, providerError("clinical_trials", "HTTP %d", resp.StatusCode)
	}

	var result struct {
		Studies []struct {
			ProtocolSection struct {
				IdentificationModule struct {
					NCTID      string `json:"nctId"`
					BriefTitle string `json:"briefTitle"`
				} `json:"identificationModule"`
				DescriptionModule struct {
					BriefSummary string `json:"briefSummary"`
				} `json:"descriptionModule"`
				ConditionsModule struct {
					Conditions []string `json:"conditions"`
				} `json:"conditionsModule"`
				DesignModule struct {
					Phases []string `json:"phases"`
				} `json:"designModule"`
				StatusModule struct {
					OverallStatus   string `json:"overallStatus"`
					StartDateStruct struct {
						Date string `json:"date"`
					} `json:"startDateStruct"`
				} `json:"statusModule"`
				SponsorCollaboratorsModule struct {
					LeadSponsor struct {
						Name string `json:"name"`
					} `json:"leadSponsor"`
				} `json:"sponsorCollaboratorsModule"`
			} `json:"protocolSection"`
		} `json:"studies"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		c.RecordFailure()
		return nil, providerError("clinical_trials", "decode: %v", err)
	}

	papers := make([]Paper, 0, len(result.Studies))
	for _, study := range result.Studies {
		id := study.ProtocolSection.IdentificationModule
		desc := study.ProtocolSection.DescriptionModule
		status := study.ProtocolSection.StatusModule
		conds := study.ProtocolSection.ConditionsModule
		phases := study.ProtocolSection.DesignModule.Phases

		title := strings.TrimSpace(id.BriefTitle)
		if title == "" {
			continue
		}

		// Build a descriptive abstract from available fields
		var absParts []string
		if len(conds.Conditions) > 0 {
			absParts = append(absParts, "Conditions: "+strings.Join(conds.Conditions, ", "))
		}
		if len(phases) > 0 {
			absParts = append(absParts, "Phase: "+strings.Join(phases, ", "))
		}
		if status.OverallStatus != "" {
			absParts = append(absParts, "Status: "+status.OverallStatus)
		}
		if desc.BriefSummary != "" {
			absParts = append(absParts, desc.BriefSummary)
		}

		link := "https://clinicaltrials.gov/study/" + id.NCTID

		year := 0
		if len(status.StartDateStruct.Date) >= 4 {
			fmt.Sscanf(status.StartDateStruct.Date[:4], "%d", &year)
		}

		papers = append(papers, Paper{
			ID:       "ct:" + id.NCTID,
			Title:    title,
			Abstract: strings.Join(absParts, "\n\n"),
			Link:     link,
			Source:   "clinical_trials",
			Year:     year,
		})
	}

	c.RecordSuccess()
	return papers, nil
}
