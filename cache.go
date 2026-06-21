package main

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

type CacheEntry struct {
	Timestamp    int64  `json:"timestamp"`
	Answer       string `json:"answer"`
	Question     string `json:"question"`
	QuestionType string `json:"question_type"`
	Options      string `json:"options"`
}

type SimpleCache struct {
	mu         sync.RWMutex
	cache      map[string]CacheEntry
	expiration int64 // Expiration time in seconds
}

func NewSimpleCache(expirationSeconds int) *SimpleCache {
	c := &SimpleCache{
		cache:      make(map[string]CacheEntry),
		expiration: int64(expirationSeconds),
	}
	c.loadFromFile() // Load from file on initialization
	return c
}

func (c *SimpleCache) generateKey(question, questionType, options string) string {
	content := fmt.Sprintf("%s|%s|%s", question, questionType, options)
	hash := md5.Sum([]byte(content))
	return hex.EncodeToString(hash[:])
}

func (c *SimpleCache) Get(question, questionType, options string) (string, bool) {
	key := c.generateKey(question, questionType, options)
	c.mu.Lock()
	defer c.mu.Unlock()
	
	entry, exists := c.cache[key]
	if !exists {
		return "", false
	}
	
	now := time.Now().Unix()
	if now - entry.Timestamp < c.expiration {
		return entry.Answer, true
	}
	
	// Cache expired, delete it and save to file
	delete(c.cache, key)
	
	// Write changes directly because lock is already held
	c.saveToFileRaw()
	return "", false
}

func (c *SimpleCache) Set(question, answer, questionType, options string) {
	key := c.generateKey(question, questionType, options)
	c.mu.Lock()
	c.cache[key] = CacheEntry{
		Timestamp:    time.Now().Unix(),
		Answer:       answer,
		Question:     question,
		QuestionType: questionType,
		Options:      options,
	}
	c.mu.Unlock()
	c.saveToFile()
}

func (c *SimpleCache) Clear() {
	c.mu.Lock()
	c.cache = make(map[string]CacheEntry)
	c.mu.Unlock()
	c.saveToFile()
}

func (c *SimpleCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.cache)
}

func (c *SimpleCache) RemoveExpired() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	
	now := time.Now().Unix()
	expiredCount := 0
	
	for key, entry := range c.cache {
		if now - entry.Timestamp >= c.expiration {
			delete(c.cache, key)
			expiredCount++
		}
	}
	
	if expiredCount > 0 {
		c.saveToFileRaw()
	}
	
	return expiredCount
}

func (c *SimpleCache) loadFromFile() {
	file, err := os.Open("cache.json")
	if err != nil {
		return // Cache file does not exist, which is fine
	}
	defer file.Close()
	
	var loadedCache map[string]CacheEntry
	if err := json.NewDecoder(file).Decode(&loadedCache); err == nil {
		c.mu.Lock()
		c.cache = loadedCache
		c.mu.Unlock()
	}
}

// saveToFileRaw writes the cache map to cache.json without locking (should be called when lock is already acquired)
func (c *SimpleCache) saveToFileRaw() {
	data, err := json.MarshalIndent(c.cache, "", "  ")
	if err != nil {
		return
	}
	os.WriteFile("cache.json", data, 0644)
}

func (c *SimpleCache) saveToFile() {
	c.mu.RLock()
	data, err := json.MarshalIndent(c.cache, "", "  ")
	c.mu.RUnlock()
	if err != nil {
		return
	}
	os.WriteFile("cache.json", data, 0644)
}
