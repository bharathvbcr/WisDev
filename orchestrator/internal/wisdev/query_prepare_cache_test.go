package wisdev

import (
	"fmt"
	"testing"
)

// TestPreparedQueryCacheEvictsWhenFull proves the prepared-query cache is
// bounded: once more than preparedQueryCacheMaxEntries distinct keys are
// stored, the cache is cleared and the size counter resets to the number of
// keys re-stored for the entry that tripped the eviction.
func TestPreparedQueryCacheEvictsWhenFull(t *testing.T) {
	ResetQueryPreparationStateForTest()
	t.Cleanup(ResetQueryPreparationStateForTest)

	earlyQuery := "early prepared query zero"
	storePreparedQuery(PreparedResearchQuery{
		Original:    earlyQuery,
		Corrected:   earlyQuery,
		SearchQuery: earlyQuery,
	})
	if _, ok := lookupPreparedQuery(earlyQuery); !ok {
		t.Fatalf("expected early prepared query to be cached before eviction")
	}

	for i := 0; i < preparedQueryCacheMaxEntries; i++ {
		query := fmt.Sprintf("distinct prepared query number %d", i)
		storePreparedQuery(PreparedResearchQuery{
			Original:    query,
			Corrected:   query,
			SearchQuery: query,
		})
	}

	if size := preparedQueryCacheSize.Load(); size > preparedQueryCacheMaxEntries {
		t.Fatalf("expected cache size counter to stay below threshold after eviction, got %d", size)
	}
	if _, ok := lookupPreparedQuery(earlyQuery); ok {
		t.Fatalf("expected early prepared query to be evicted after exceeding %d entries", preparedQueryCacheMaxEntries)
	}

	lastQuery := fmt.Sprintf("distinct prepared query number %d", preparedQueryCacheMaxEntries-1)
	if _, ok := lookupPreparedQuery(lastQuery); !ok {
		t.Fatalf("expected the entry that tripped eviction to be re-stored after the reset")
	}
	if size := preparedQueryCacheSize.Load(); size != 1 {
		t.Fatalf("expected size counter to equal re-stored key count (1), got %d", size)
	}
}

// TestResetQueryPreparationStateForTestResetsCounter ensures the test reset
// hook also clears the eviction counter.
func TestResetQueryPreparationStateForTestResetsCounter(t *testing.T) {
	storePreparedQuery(PreparedResearchQuery{
		Original:    "counter reset probe query",
		Corrected:   "counter reset probe query",
		SearchQuery: "counter reset probe query",
	})
	if preparedQueryCacheSize.Load() == 0 {
		t.Fatal("expected counter to be non-zero after storing a prepared query")
	}
	ResetQueryPreparationStateForTest()
	if size := preparedQueryCacheSize.Load(); size != 0 {
		t.Fatalf("expected counter reset to zero, got %d", size)
	}
	if _, ok := lookupPreparedQuery("counter reset probe query"); ok {
		t.Fatal("expected prepared query cache to be cleared by reset")
	}
}
