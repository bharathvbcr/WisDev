package search

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNASAADSProvider_Search(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"response": {
				"docs": [
					{
						"title": ["Cosmic Rays"],
						"bibcode": "2023CR...1...1A",
						"author": ["John Doe"],
						"pubdate": "2023-01-00",
						"abstract": "Study of cosmic rays",
						"doi": ["10.1234/cr.1"]
					}
				]
			}
		}`))
	}))
	defer server.Close()

	p := NewNASAADSProvider()
	p.apiKey = "test-key"
	p.baseURL = server.URL

	papers, err := p.Search(context.Background(), "cosmic rays", SearchOpts{Limit: 1})
	assert.NoError(t, err)
	assert.Len(t, papers, 1)
	assert.Equal(t, "Cosmic Rays", papers[0].Title)
	assert.Equal(t, "nasa_ads", papers[0].Source)
	assert.Equal(t, []string{"nasa_ads"}, papers[0].SourceApis)
}

func TestPapersWithCodeProvider_Search(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"results": [
				{
					"title": "ResNet",
					"paper_url": "https://arxiv.org/abs/1512.03385",
					"abstract": "Deep residual learning",
					"authors": ["Kaiming He"],
					"published": "2015-12-10",
					"repository": "https://github.com/microsoft/resnet"
				}
			]
		}`))
	}))
	defer server.Close()

	p := NewPapersWithCodeProvider()
	p.baseURL = server.URL

	papers, err := p.Search(context.Background(), "resnet", SearchOpts{Limit: 1})
	assert.NoError(t, err)
	assert.Len(t, papers, 1)
	assert.Equal(t, "ResNet", papers[0].Title)
}
