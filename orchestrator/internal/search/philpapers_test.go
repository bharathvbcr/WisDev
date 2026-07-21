package search

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPhilPapersProviderBranches(t *testing.T) {
	origClient := SharedHTTPClient
	defer func() { SharedHTTPClient = origClient }()

	t.Run("Search Success and Fallback Link", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{"entries":[{"id":"p1","title":"Phil Paper","abstract":"Abstract","authors":["Plato"],"pub":"Mind","openAccessUrl":"https://oa.example/p1.pdf","year":2024,"citations":12},{"id":"skip","title":"","abstract":"Skip"}]}`)
				return rec.Result(), nil
			}),
		}
		p := NewPhilPapersProvider()
		papers, err := p.Search(context.Background(), "test", SearchOpts{Limit: 5, YearFrom: 2020, YearTo: 2024})
		assert.NoError(t, err)
		assert.Len(t, papers, 2)
		assert.Equal(t, "philpapers-p1", papers[0].ID)
		assert.Equal(t, "https://philpapers.org/rec/p1", papers[0].Link)
		assert.Equal(t, "philpapers", papers[0].Source)
		assert.Equal(t, []string{"philpapers"}, papers[0].SourceApis)
		assert.Equal(t, "Mind", papers[0].Venue)
		assert.Equal(t, "https://oa.example/p1.pdf", papers[0].OpenAccessUrl)
	})

	t.Run("Transport Status and Decode Errors", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Query().Get("searchStr") {
				case "transport":
					return nil, fmt.Errorf("boom")
				case "status":
					rec := httptest.NewRecorder()
					rec.WriteHeader(http.StatusBadGateway)
					return rec.Result(), nil
				default:
					rec := httptest.NewRecorder()
					fmt.Fprint(rec, `invalid`)
					return rec.Result(), nil
				}
			}),
		}
		p := NewPhilPapersProvider()
		_, err := p.Search(context.Background(), "transport", SearchOpts{})
		assert.Error(t, err)
		_, err = p.Search(context.Background(), "status", SearchOpts{})
		assert.Error(t, err)
		_, err = p.Search(context.Background(), "decode", SearchOpts{})
		assert.Error(t, err)
	})
}
