package main

import (
	"database/sql"
	"os"
	"testing"

	_ "modernc.org/sqlite"
)

func TestOptionShuffledCache(t *testing.T) {
	// Clean up any existing test db
	testDBPath := "./test_cache.db"
	os.Remove(testDBPath)
	defer os.Remove(testDBPath)

	db, err := sql.Open("sqlite", testDBPath)
	if err != nil {
		t.Fatalf("Failed to open test database: %v", err)
	}
	defer db.Close()

	// Create table
	query := `
	CREATE TABLE IF NOT EXISTS cache (
		key TEXT PRIMARY KEY,
		question TEXT,
		question_type TEXT,
		options TEXT,
		answer TEXT,
		timestamp INTEGER
	);`
	if _, err := db.Exec(query); err != nil {
		t.Fatalf("Failed to create table: %v", err)
	}

	c := NewSQLiteCache(db)

	question := "What is the capital of France?"
	questionType := "single"
	originalOptions := "A. Paris\nB. London\nC. Berlin"
	originalAnswer := "A"

	// Cache the answer with the original options
	c.Set(question, originalAnswer, questionType, originalOptions)

	// Test case 1: Query with identical options order
	ans1, found1 := c.Get(question, questionType, originalOptions)
	if !found1 {
		t.Errorf("Expected cache hit for identical options, but got miss")
	}
	if ans1 != "A" {
		t.Errorf("Expected answer 'A' for identical options, got '%s'", ans1)
	}

	// Test case 2: Query with shuffled options order (Paris is now B)
	shuffledOptions := "A. London\nB. Paris\nC. Berlin"
	ans2, found2 := c.Get(question, questionType, shuffledOptions)
	if !found2 {
		t.Errorf("Expected cache hit for shuffled options, but got miss")
	}
	if ans2 != "B" {
		t.Errorf("Expected mapped answer to be 'B' (Paris), but got '%s'", ans2)
	}

	// Test case 3: Query with multiple choice question shuffling
	mcQuestion := "Select primary colors"
	mcType := "multiple"
	mcOriginalOptions := "A. Red\nB. Green\nC. Blue\nD. Yellow"
	mcOriginalAnswer := "A#C" // Red and Blue

	c.Set(mcQuestion, mcOriginalAnswer, mcType, mcOriginalOptions)

	// Shuffled multiple choice: Red is C, Blue is A
	mcShuffledOptions := "A. Blue\nB. Yellow\nC. Red\nD. Green"
	ans3, found3 := c.Get(mcQuestion, mcType, mcShuffledOptions)
	if !found3 {
		t.Errorf("Expected cache hit for shuffled MC options, but got miss")
	}
	// Red (C) and Blue (A) -> Sorted mapped answer should be A#C
	if ans3 != "A#C" {
		t.Errorf("Expected mapped MC answer to be 'A#C', but got '%s'", ans3)
	}
}
