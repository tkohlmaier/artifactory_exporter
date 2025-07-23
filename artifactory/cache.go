package artifactory

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type CacheEntry struct {
	data      *ApiResponse
	timestamp time.Time
}

// ResponseCache stores API responses and allows thread-safe access.
type ResponseCache struct {
	mutex sync.RWMutex
	data  map[string]CacheEntry
	ttl   time.Duration
}

func (r *ResponseCache) Prune() int {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	var removed = 0
	now := time.Now()
	for key, entry := range r.data {
		if now.Sub(entry.timestamp) > r.ttl {
			delete(r.data, key)
			removed++
		}
	}
	return removed
}

func NewResponseCache(useCache bool, ttl time.Duration) *ResponseCache {
	if !useCache {
		return nil
	}
	return &ResponseCache{
		data: make(map[string]CacheEntry),
		ttl:  ttl,
	}
}

func (rc *ResponseCache) GetCachedResponse(key string) (*ApiResponse, bool) {
	rc.mutex.RLock()
	defer rc.mutex.RUnlock()

	resp, exists := rc.data[key]
	if !exists {
		return nil, false
	} else if time.Since(resp.timestamp) > rc.ttl {
		// Entry is expired, ignore it
		return nil, false
	}
	return resp.data, true
}

func (rc *ResponseCache) SetCachedResponse(key string, response *ApiResponse) {
	rc.mutex.Lock()
	defer rc.mutex.Unlock()

	rc.data[key] = CacheEntry{
		data:      response,
		timestamp: time.Now(),
	}
}

// Cached is a wrapper for http requests that allows caching of API responses.
// The actual request runs in a separate goroutine which will update the cache whenever a response is received.
// If we hit a timeout, we can instead return a cached response if available.
type Cached struct {
	errors      chan error
	responses   chan *ApiResponse
	timeout     context.Context
	cancel      context.CancelFunc
	stopTimeout func() bool

	client   *Client
	cacheKey string
}

func (c *Client) NewCached(cacheKey string) *Cached {
	errors := make(chan error, 1)
	responses := make(chan *ApiResponse, 1)
	timeout, cancel := context.WithTimeout(context.Background(), c.cacheTimeout)
	stopTimeout := context.AfterFunc(timeout, func() {
		c.logger.Warn("Cache request timed out", "timeout", c.cacheTimeout)
		errors <- fmt.Errorf("request timed out after %d seconds", int(c.cacheTimeout.Seconds()))
	})

	return &Cached{
		errors:      errors,
		responses:   responses,
		timeout:     timeout,
		cancel:      cancel,
		stopTimeout: stopTimeout,
		client:      c,
		cacheKey:    cacheKey,
	}
}

func (c *Cached) Close() {
	if !c.stopTimeout() {
		// timeout func already triggered
		<-c.errors
	}
	c.cancel()
	close(c.errors)
	close(c.responses)
}

func (c *Cached) CacheResponse(response *ApiResponse) {
	if c.client.responseCache != nil {
		c.client.logger.Debug("Caching response for key", "key", c.cacheKey)
		c.client.responseCache.SetCachedResponse(c.cacheKey, response)
	}
}

func (c *Cached) GetCachedResponse() (*ApiResponse, bool) {
	if c.client.responseCache != nil {
		c.client.logger.Debug("Getting cached response for key", "key", c.cacheKey)
		return c.client.responseCache.GetCachedResponse(c.cacheKey)
	}
	return nil, false
}
