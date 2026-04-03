package search

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
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
		os.Unsetenv("CORE_API_KEY")
		p := NewCOREProvider()
		papers, err := p.Search(context.Background(), "test", SearchOpts{})
		assert.NoError(t, err)
		assert.Empty(t, papers)
	})

	t.Run("Search Success", func(t *testing.T) {
		os.Setenv("CORE_API_KEY", "test-key")
		defer os.Unsetenv("CORE_API_KEY")

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
}
