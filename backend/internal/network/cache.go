package network

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type cacheEntry struct {
	Data []byte
	ETag string
	Time time.Time
}
type MapCache struct {
	Dir string
	mu  sync.Mutex
}

func (c *MapCache) Get(url string) ([]byte, string, time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, e := os.ReadFile(filepath.Join(c.Dir, "derp-cache.json"))
	if e != nil {
		return nil, "", time.Time{}, false
	}
	var d cacheEntry
	if json.Unmarshal(b, &d) != nil {
		return nil, "", time.Time{}, false
	}
	return d.Data, d.ETag, d.Time, true
}
func (c *MapCache) Put(url string, data []byte, etag string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, e := json.Marshal(cacheEntry{data, etag, time.Now()})
	if e != nil {
		return e
	}
	return os.WriteFile(filepath.Join(c.Dir, "derp-cache.json"), b, 0600)
}
