package main

import (
	"database/sql"
	"math/rand"
	"os"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestUserDatabaseRandomShuffledHits(t *testing.T) {
	dbPath := "./cache.db"
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Skip("cache.db not found, skipping user database random test")
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("Failed to open cache.db: %v", err)
	}
	defer db.Close()

	// Query rows
	rows, err := db.Query("SELECT question, question_type, options, answer FROM cache WHERE question_type = 'single' OR question_type = 'multiple'")
	if err != nil {
		t.Fatalf("Failed to query cache.db: %v", err)
	}
	defer rows.Close()

	var allRows []struct {
		Question     string
		QuestionType string
		Options      string
		Answer       string
	}
	for rows.Next() {
		var r struct {
			Question     string
			QuestionType string
			Options      string
			Answer       string
		}
		if err := rows.Scan(&r.Question, &r.QuestionType, &r.Options, &r.Answer); err == nil {
			if strings.TrimSpace(r.Options) != "" {
				allRows = append(allRows, r)
			}
		}
	}

	if len(allRows) == 0 {
		t.Skip("No suitable cached questions found with options in cache.db")
	}

	t.Logf("Found %d cached questions. Selecting 5 random questions for verification...", len(allRows))
	rand.Seed(time.Now().UnixNano())
	indices := rand.Perm(len(allRows))
	if len(indices) > 5 {
		indices = indices[:5]
	}

	cache := NewSQLiteCache(db)
	successCount := 0

	for i, idx := range indices {
		r := allRows[idx]

		// Shuffle options
		optLines := strings.Split(r.Options, "\n")
		var trimmed []string
		for _, l := range optLines {
			l = strings.TrimSpace(l)
			if l != "" {
				trimmed = append(trimmed, l)
			}
		}
		rand.Shuffle(len(trimmed), func(i, j int) {
			trimmed[i], trimmed[j] = trimmed[j], trimmed[i]
		})
		shuffledOpts := strings.Join(trimmed, "\n")

		ans, found := cache.Get(r.Question, r.QuestionType, shuffledOpts)
		if found {
			t.Logf("[%d] MATCHED Question: %q", i+1, r.Question)
			t.Logf("     Original Answer: %q", r.Answer)
			t.Logf("     Returned Answer: %q", ans)
			successCount++
		} else {
			t.Errorf("[%d] MISSED Question: %q", i+1, r.Question)
			t.Logf("     Original Options: %q", r.Options)
			t.Logf("     Shuffled Options: %q", shuffledOpts)
			t.Logf("     Norm Original:    %q", normalizeOptions(r.Options))
			t.Logf("     Norm Shuffled:    %q", normalizeOptions(shuffledOpts))
		}
	}

	t.Logf("Completed user cache verification: hit %d/5", successCount)
	if successCount < 5 {
		t.Errorf("Expected 5/5 hits, got %d/5", successCount)
	}
}
