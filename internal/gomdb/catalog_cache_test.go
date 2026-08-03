package gomdb

import (
	"testing"
)

func TestCatalogCacheConsistency(t *testing.T) {
	catalogCacheMu.Lock()
	clear(catalogCache)
	catalogCacheMu.Unlock()

	list := func() map[string]bool {
		q, err := OpenQuery("../../testdata/people.mdb", "LIST TABLES")
		if err != nil {
			t.Fatal(err)
		}
		defer q.Close()
		names := map[string]bool{}
		for {
			ok, err := q.Next()
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				break
			}
			names[q.Value(0)] = true
		}
		return names
	}

	first := list()  // populates the cache
	second := list() // cache hit
	if len(first) == 0 || len(first) != len(second) {
		t.Fatalf("catalog cache mismatch: first=%d second=%d", len(first), len(second))
	}
	for name := range first {
		if !second[name] {
			t.Fatalf("cached catalog lost table %q", name)
		}
	}

	catalogCacheMu.Lock()
	_, ok := catalogCache["../../testdata/people.mdb"]
	catalogCacheMu.Unlock()
	if !ok {
		t.Fatal("catalog cache was not populated")
	}
}
