package storage

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"api-monitor/internal/checker"
)

func result(url string, i int) checker.CheckResult {
	return checker.CheckResult{
		URL:          url,
		StatusCode:   200,
		ResponseTime: time.Duration(i) * time.Millisecond,
		IsHealthy:    true,
		CheckedAt:    time.Unix(int64(i), 0),
	}
}

func TestMemoryStore_ImplementsStore(t *testing.T) {
	var _ Store = NewMemoryStore(1)
}

func TestMemoryStore_EmptyURL(t *testing.T) {
	m := NewMemoryStore(10)
	got, err := m.GetRecentResults("https://nothing", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %d results for unknown URL", len(got))
	}
}

func TestMemoryStore_NewestFirstAndLimit(t *testing.T) {
	m := NewMemoryStore(10)
	batch := []checker.CheckResult{}
	for i := 1; i <= 5; i++ {
		batch = append(batch, result("https://a", i))
	}
	batch = append(batch, result("https://b", 99))
	if err := m.SaveResults(batch); err != nil {
		t.Fatal(err)
	}

	got, err := m.GetRecentResults("https://a", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d results, want 3", len(got))
	}
	for i, want := range []int{5, 4, 3} {
		if got[i].ResponseTime != time.Duration(want)*time.Millisecond {
			t.Errorf("got[%d].ResponseTime = %v, want %dms (newest first)", i, got[i].ResponseTime, want)
		}
		if got[i].URL != "https://a" {
			t.Errorf("got[%d].URL = %q, results leaked across URLs", i, got[i].URL)
		}
	}

	// limit <= 0 returns everything.
	all, _ := m.GetRecentResults("https://a", 0)
	if len(all) != 5 {
		t.Errorf("limit 0 returned %d, want all 5", len(all))
	}
	// limit larger than stored is clamped.
	more, _ := m.GetRecentResults("https://a", 100)
	if len(more) != 5 {
		t.Errorf("limit 100 returned %d, want 5", len(more))
	}
}

func TestMemoryStore_EvictsOldest(t *testing.T) {
	m := NewMemoryStore(3)
	for i := 1; i <= 5; i++ {
		if err := m.SaveResults([]checker.CheckResult{result("https://a", i)}); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := m.GetRecentResults("https://a", 10)
	if len(got) != 3 {
		t.Fatalf("got %d results, want capacity 3", len(got))
	}
	if got[0].ResponseTime != 5*time.Millisecond || got[2].ResponseTime != 3*time.Millisecond {
		t.Errorf("expected results 5,4,3 after eviction, got %v", got)
	}
}

func TestMemoryStore_DefaultCapacity(t *testing.T) {
	m := NewMemoryStore(0)
	if m.maxPerURL != DefaultMemoryCapacity {
		t.Errorf("maxPerURL = %d, want %d", m.maxPerURL, DefaultMemoryCapacity)
	}
}

func TestMemoryStore_ConcurrentWrites(t *testing.T) {
	m := NewMemoryStore(1000)
	var wg sync.WaitGroup
	for g := 0; g < 10; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				_ = m.SaveResults([]checker.CheckResult{result(fmt.Sprintf("https://u%d", g%2), i)})
				_, _ = m.GetRecentResults("https://u0", 5)
			}
		}(g)
	}
	wg.Wait()
	a, _ := m.GetRecentResults("https://u0", 0)
	b, _ := m.GetRecentResults("https://u1", 0)
	if len(a)+len(b) != 500 {
		t.Errorf("stored %d results, want 500", len(a)+len(b))
	}
}
