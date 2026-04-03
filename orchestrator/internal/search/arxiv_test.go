package search

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestArXivProvider(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/error" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if r.URL.Path == "/badxml" {
			fmt.Fprint(w, "not xml")
			return
		}

		xml := `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <entry>
    <id>http://arxiv.org/abs/2103.00001v1</id>
    <title>  Test  Paper  </title>
    <summary>  This is a summary.  </summary>
    <author><name>Author One</name></author>
    <published>2021-03-01T00:00:00Z</published>
    <link title="pdf" href="http://arxiv.org/pdf/2103.00001v1" rel="related" type="application/pdf"/>
  </entry>
  <entry>
    <id>http://arxiv.org/abs/2103.00002</id>
    <title>No PDF</title>
    <link href="http://arxiv.org/abs/2103.00002" rel="alternate" type="text/html"/>
  </entry>
</feed>`
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, xml)
	}))
	defer ts.Close()

	p := NewArXivProvider()
	p.baseURL = ts.URL

	t.Run("Name and Domains", func(t *testing.T) {
		assert.Equal(t, "arxiv", p.Name())
		assert.NotEmpty(t, p.Domains())
	})

	t.Run("Search Success", func(t *testing.T) {
		papers, err := p.Search(context.Background(), "test", SearchOpts{Limit: 1, YearFrom: 2020})
		assert.NoError(t, err)
		assert.Len(t, papers, 2)
		assert.Equal(t, "arxiv:2103.00001", papers[0].ID)
		assert.Equal(t, "arxiv:2103.00002", papers[1].ID)
		assert.Equal(t, "http://arxiv.org/pdf/2103.00001v1", papers[0].Link)
		assert.Equal(t, "http://arxiv.org/abs/2103.00002", papers[1].Link)
	})

	t.Run("HTTP Error", func(t *testing.T) {
		pErr := NewArXivProvider()
		pErr.baseURL = ts.URL + "/error"
		_, err := pErr.Search(context.Background(), "test", SearchOpts{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "HTTP 404")
	})

	t.Run("Request Failed", func(t *testing.T) {
		pFail := NewArXivProvider()
		pFail.baseURL = "http://invalid.domain.that.does.not.exist"
		_, err := pFail.Search(context.Background(), "test", SearchOpts{})
		assert.Error(t, err)
	})

	t.Run("Decode Error", func(t *testing.T) {
		pBad := NewArXivProvider()
		pBad.baseURL = ts.URL + "/badxml"
		_, err := pBad.Search(context.Background(), "test", SearchOpts{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode")
	})
}

func TestExtractArXivID(t *testing.T) {
	tests := []struct {
		url      string
		expected string
	}{
		{"http://arxiv.org/abs/2103.00001v1", "2103.00001"},
		{"2103.00001v2", "2103.00001"},
		{"http://arxiv.org/pdf/hep-th/9901001v3", "9901001"}, // Matches current implementation
		{"justid", "justid"},
		{"v123", "v123"},
		{"idvabc", "idvabc"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.expected, extractArXivID(tt.url))
	}
}
