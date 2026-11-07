package op

import (
	"sync"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

// A reader that misses the cache reads the database and then fills the cache.
// An admin save in that window bumps the generation and clears the cache; the
// fill must not put the value read before the save back into a cache that never
// expires.
func TestSettingCacheFillSkippedAfterUpdate(t *testing.T) {
	const key = "setting_cache_fill_guard"
	Cache.ClearAll()

	stale := &model.SettingItem{Key: key, Value: "before"}
	gen := settingCacheGeneration()

	// The save lands between the reader's fetch and its fill.
	SettingCacheUpdate()
	settingCacheF(gen, stale)

	if item, ok := Cache.GetSetting(key); ok {
		t.Fatalf("a fill from before the save repopulated the cache with %q", item.Value)
	}

	// A fill from the current generation still works.
	settingCacheF(settingCacheGeneration(), stale)
	if item, ok := Cache.GetSetting(key); !ok || item.Value != "before" {
		t.Fatalf("the cache no longer accepts a fresh fill: ok=%v item=%+v", ok, item)
	}
}

// The same rule for grouped reads, which is what the admin settings page and the
// public settings endpoint use.
func TestSettingGroupCacheFillSkippedAfterUpdate(t *testing.T) {
	const group = "GROUP_TEST_FILL_GUARD"
	Cache.ClearAll()

	items := []model.SettingItem{{Key: "k", Value: "before"}}
	gen := settingCacheGeneration()
	SettingCacheUpdate()
	settingGroupCacheF(gen, group, items)

	if got, ok := Cache.GetSettingGroup(group); ok {
		t.Fatalf("a stale group fill repopulated the cache with %d items", len(got))
	}
}

// The counter itself has to be race-free: fills happen on request goroutines
// while a save bumps the generation.
func TestSettingCacheGenerationIsRaceFree(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for n := 0; n < 200; n++ {
				settingCacheF(settingCacheGeneration(), &model.SettingItem{Key: "k", Value: "v"})
			}
		}()
		go func() {
			defer wg.Done()
			for n := 0; n < 50; n++ {
				SettingCacheUpdate()
			}
		}()
	}
	wg.Wait()
}
