package search

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRePECProvider_Search(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"items": [
				{
					"title": "Inflation and Interest Rates",
					"handle": "repec:abc:123",
					"author": ["Jane Smith"],
					"year": "2022",
					"abstract": "Analysis of inflation"
				}
			]
		}`))
	}))
	defer server.Close()

	p := NewRePECProvider()
	p.baseURL = server.URL

	papers, err := p.Search(context.Background(), "inflation", SearchOpts{Limit: 1})
	assert.NoError(t, err)
	assert.Len(t, papers, 1)
	assert.Equal(t, "Inflation and Interest Rates", papers[0].Title)
	assert.Equal(t, "repec", papers[0].Source)
	assert.Equal(t, []string{"repec"}, papers[0].SourceApis)
}

func TestPhilPapersProvider_Search(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"entries": [
				{
					"title": "Categorical Imperative",
					"id": "abc-123",
					"authors": ["Immanuel Kant"],
					"year": 1785,
					"abstract": "Act only according to that maxim..."
				}
			]
		}`))
	}))
	defer server.Close()

	p := NewPhilPapersProvider()
	p.baseURL = server.URL

	papers, err := p.Search(context.Background(), "kant", SearchOpts{Limit: 1})
	assert.NoError(t, err)
	assert.Len(t, papers, 1)
	assert.Equal(t, "Categorical Imperative", papers[0].Title)
	assert.Equal(t, "philpapers", papers[0].Source)
	assert.Equal(t, []string{"philpapers"}, papers[0].SourceApis)
}
