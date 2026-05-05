package search

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/resilience"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCOREProvider(t *testing.T) {
	origClient := SharedHTTPClient
	defer func() { SharedHTTPClient = origClient }()

	t.Run("Name and Domains", func(t *testing.T) {
		p := NewCOREProvider()
		assert.Equal(t, "core", p.Name())
		assert.Empty(t, p.Domains())
	})

	t.Run("Search - No API Key", func(t *testing.T) {
		restore := resilience.MockProjectIDForTest("")
		defer restore()

		os.Unsetenv("CORE_API_KEY")
		// Unset project env vars to prevent Secret Manager lookup
		os.Unsetenv("GOOGLE_CLOUD_PROJECT")
		os.Unsetenv("GCLOUD_PROJECT")
		os.Unsetenv("GCP_PROJECT_ID")
		os.Unsetenv("CLOUDSDK_CORE_PROJECT")
		resilience.InvalidateSecret("CORE_API_KEY")
		p := NewCOREProvider()
		papers, err := p.Search(context.Background(), "test", SearchOpts{})
		assert.NoError(t, err)
		assert.Empty(t, papers)
	})

	t.Run("Search Success", func(t *testing.T) {
		os.Setenv("CORE_API_KEY", "test-key")
		resilience.InvalidateSecret("CORE_API_KEY")
		defer os.Unsetenv("CORE_API_KEY")
		defer resilience.InvalidateSecret("CORE_API_KEY")

		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				assert.Equal(t, "Bearer test-key", req.Header.Get("Authorization"))
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{"results":[{"id":123,"title":"CORE Paper","downloadUrl":"http://dl"}]}`)
				return rec.Result(), nil
			}),
		}
		p := NewCOREProvider()
		papers, err := p.Search(context.Background(), "test", SearchOpts{YearFrom: 2020})
		assert.NoError(t, err)
		assert.Len(t, papers, 1)
		assert.Equal(t, "core:123", papers[0].ID)
		assert.Equal(t, []string{"core"}, papers[0].SourceApis)
	})

	t.Run("Search falls back to DOI link and includes year range", func(t *testing.T) {
		os.Setenv("CORE_API_KEY", "test-key")
		resilience.InvalidateSecret("CORE_API_KEY")
		defer os.Unsetenv("CORE_API_KEY")
		defer resilience.InvalidateSecret("CORE_API_KEY")

		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				assert.Contains(t, req.URL.RawQuery, "yearPublished%3E%3D2020")
				assert.Contains(t, req.URL.RawQuery, "yearPublished%3C%3D2022")
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{"results":[{"id":7,"title":"CORE DOI Paper","doi":"10.1/example","yearPublished":2022}]}`)
				return rec.Result(), nil
			}),
		}
		p := NewCOREProvider()
		papers, err := p.Search(context.Background(), "test", SearchOpts{YearFrom: 2020, YearTo: 2022})
		assert.NoError(t, err)
		assert.Len(t, papers, 1)
		assert.Equal(t, "https://doi.org/10.1/example", papers[0].Link)
	})

	t.Run("HTTP Error", func(t *testing.T) {
		os.Setenv("CORE_API_KEY", "test-key")
		defer os.Unsetenv("CORE_API_KEY")

		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.WriteHeader(http.StatusServiceUnavailable)
				return rec.Result(), nil
			}),
		}
		p := NewCOREProvider()
		_, err := p.Search(context.Background(), "q", SearchOpts{})
		assert.Error(t, err)
	})

	t.Run("Transport and decode errors", func(t *testing.T) {
		os.Setenv("CORE_API_KEY", "test-key")
		resilience.InvalidateSecret("CORE_API_KEY")
		defer os.Unsetenv("CORE_API_KEY")
		defer resilience.InvalidateSecret("CORE_API_KEY")

		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return nil, fmt.Errorf("transport failed")
			}),
		}
		p := NewCOREProvider()
		_, err := p.Search(context.Background(), "q", SearchOpts{})
		assert.Error(t, err)

		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `invalid`)
				return rec.Result(), nil
			}),
		}
		_, err = p.Search(context.Background(), "q", SearchOpts{})
		assert.Error(t, err)
	})

	t.Run("Build Request Error", func(t *testing.T) {
		os.Setenv("CORE_API_KEY", "test-key")
		resilience.InvalidateSecret("CORE_API_KEY")
		defer os.Unsetenv("CORE_API_KEY")
		defer resilience.InvalidateSecret("CORE_API_KEY")

		p := NewCOREProvider()
		p.baseURL = "http://[::1"
		_, err := p.Search(context.Background(), "q", SearchOpts{})
		assert.Error(t, err)
	})
}
