package rag

import (
	"math"
	"sort"
	"strings"
	"sync"
)

// BM25 implements the BM25 ranking algorithm.
type BM25 struct {
	k1 float64
	b  float64
	
	mu        sync.RWMutex
	docs      []string
	docIds    []string
	docFreqs  map[string]int
	avgDocLen float64
	numDocs   float64
	
	// Precomputed for faster search
	tokenizedDocs [][]string
}

// NewBM25 creates a new BM25 ranker.
func NewBM25() *BM25 {
	return &BM25{
		k1:       1.5,
		b:        0.75,
		docFreqs: make(map[string]int),
	}
}

// IndexDocuments indexes a set of documents.
func (b *BM25) IndexDocuments(documents []string, docIds []string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.docs = documents
	b.docIds = docIds
	b.tokenizedDocs = make([][]string, len(documents))
	b.docFreqs = make(map[string]int)

	totalLen := 0
	for i, doc := range documents {
		terms := b.tokenize(doc)
		b.tokenizedDocs[i] = terms
		totalLen += len(terms)

		uniqueTerms := make(map[string]struct{})
		for _, term := range terms {
			uniqueTerms[term] = struct{}{}
		}
		for term := range uniqueTerms {
			b.docFreqs[term]++
		}
	}

	if len(documents) > 0 {
		b.avgDocLen = float64(totalLen) / float64(len(documents))
		b.numDocs = float64(len(documents))
	}
}

// Score ranks provided documents based on the query (stateless).
func (b *BM25) Score(query string, documents []string) []float64 {
	if len(documents) == 0 {
		return nil
	}

	queryTerms := b.tokenize(query)
	docTerms := make([][]string, len(documents))
	docFreqs := make(map[string]int)

	totalLen := 0
	for i, doc := range documents {
		terms := b.tokenize(doc)
		docTerms[i] = terms
		totalLen += len(terms)

		uniqueTerms := make(map[string]struct{})
		for _, term := range terms {
			uniqueTerms[term] = struct{}{}
		}
		for term := range uniqueTerms {
			docFreqs[term]++
		}
	}

	avgDocLen := float64(totalLen) / float64(len(documents))
	numDocs := float64(len(documents))

	scores := make([]float64, len(documents))
	for i, terms := range docTerms {
		docLen := float64(len(terms))
		termFreqs := make(map[string]int)
		for _, term := range terms {
			termFreqs[term]++
		}

		var score float64
		for _, qTerm := range queryTerms {
			df := float64(docFreqs[qTerm])
			if df == 0 {
				continue
			}

			idf := math.Log((numDocs-df+0.5)/(df+0.5) + 1.0)
			tf := float64(termFreqs[qTerm])

			score += idf * (tf * (b.k1 + 1)) / (tf + b.k1*(1-b.b+b.b*docLen/avgDocLen))
		}
		scores[i] = score
	}

	return scores
}


// Search performs a BM25 search and returns results.
func (b *BM25) Search(query string, topK int) []BM25SearchResult {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if b.numDocs == 0 {
		return nil
	}

	queryTerms := b.tokenize(query)
	scores := make([]float64, len(b.docs))

	for i, terms := range b.tokenizedDocs {
		docLen := float64(len(terms))
		termFreqs := make(map[string]int)
		for _, term := range terms {
			termFreqs[term]++
		}

		var score float64
		for _, qTerm := range queryTerms {
			df := float64(b.docFreqs[qTerm])
			if df == 0 {
				continue
			}

			// IDF calculation
			idf := math.Log((b.numDocs-df+0.5)/(df+0.5) + 1.0)
			tf := float64(termFreqs[qTerm])

			// BM25 formula
			score += idf * (tf * (b.k1 + 1)) / (tf + b.k1*(1-b.b+b.b*docLen/b.avgDocLen))
		}
		scores[i] = score
	}

	// Sort and return topK
	type item struct {
		id    string
		score float64
	}
	items := make([]item, 0, len(b.docs))
	for i, score := range scores {
		if score > 0 {
			items = append(items, item{b.docIds[i], score})
		}
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].score > items[j].score
	})

	if len(items) > topK {
		items = items[:topK]
	}

	results := make([]BM25SearchResult, len(items))
	for i, it := range items {
		results[i] = BM25SearchResult{
			DocID: it.id,
			Score: it.score,
		}
	}

	return results
}

func (b *BM25) tokenize(text string) []string {
	text = strings.ToLower(text)
	// Basic tokenization: split by spaces and punctuation
	f := func(c rune) bool {
		return !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9'))
	}
	return strings.FieldsFunc(text, f)
}

type BM25SearchResult struct {
	DocID string  `json:"doc_id"`
	Score float64 `json:"score"`
}
