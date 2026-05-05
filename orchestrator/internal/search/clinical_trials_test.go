package search

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClinicalTrialsProviderBranches(t *testing.T) {
	origClient := SharedHTTPClient
	defer func() { SharedHTTPClient = origClient }()

	t.Run("Search Success and Blank Title Skip", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				assert.Equal(t, "10", req.URL.Query().Get("pageSize"))
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{"studies":[
					{"protocolSection":{"identificationModule":{"nctId":"NCT1","briefTitle":"Study One"},"descriptionModule":{"briefSummary":"Summary"},"conditionsModule":{"conditions":["Condition A"]},"designModule":{"phases":["Phase 2"]},"statusModule":{"overallStatus":"Recruiting","startDateStruct":{"date":"2024-01-15"}},"sponsorCollaboratorsModule":{"leadSponsor":{"name":"Sponsor"}}}},
					{"protocolSection":{"identificationModule":{"nctId":"NCT2","briefTitle":""},"statusModule":{"startDateStruct":{"date":"2023-01-01"}}}}
				]}`)
				return rec.Result(), nil
			}),
		}
		p := NewClinicalTrialsProvider()
		papers, err := p.Search(context.Background(), "test", SearchOpts{})
		assert.NoError(t, err)
		assert.Len(t, papers, 1)
		assert.Equal(t, "ct:NCT1", papers[0].ID)
		assert.Contains(t, papers[0].Abstract, "Conditions: Condition A")
		assert.Equal(t, 2024, papers[0].Year)
		assert.Equal(t, []string{"clinical_trials"}, papers[0].SourceApis)
	})

	t.Run("Request Status Decode and Build Errors", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Query().Get("query.term") {
				case "request":
					return nil, fmt.Errorf("boom")
				case "status":
					rec := httptest.NewRecorder()
					rec.WriteHeader(http.StatusServiceUnavailable)
					return rec.Result(), nil
				case "decode":
					rec := httptest.NewRecorder()
					fmt.Fprint(rec, `invalid`)
					return rec.Result(), nil
				default:
					rec := httptest.NewRecorder()
					rec.Header().Set("Content-Type", "application/json")
					fmt.Fprint(rec, `{"studies":[]}`)
					return rec.Result(), nil
				}
			}),
		}
		p := NewClinicalTrialsProvider()
		_, err := p.Search(context.Background(), "request", SearchOpts{})
		assert.Error(t, err)
		_, err = p.Search(context.Background(), "status", SearchOpts{})
		assert.Error(t, err)
		_, err = p.Search(context.Background(), "decode", SearchOpts{})
		assert.Error(t, err)

		bad := NewClinicalTrialsProvider()
		bad.baseURL = "://bad"
		_, err = bad.Search(context.Background(), "query", SearchOpts{})
		assert.Error(t, err)
	})
}
