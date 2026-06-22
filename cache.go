package main

import (
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

type SQLiteCache struct {
	db *sql.DB
}

func NewSQLiteCache(db *sql.DB) *SQLiteCache {
	return &SQLiteCache{db: db}
}

var optionRegex = regexp.MustCompile(`^[\[\(\s]*([A-Za-z])(?:[\]\)\s]+|[\.、\:\-\s]+)(.*)$`)

func extractOptionPrefixAndText(line string) (string, string) {
	matches := optionRegex.FindStringSubmatch(line)
	if len(matches) == 3 {
		return strings.ToUpper(matches[1]), strings.TrimSpace(matches[2])
	}
	return "", ""
}

func cleanOptionText(text string) string {
	text = strings.ToLower(text)
	var sb strings.Builder
	for _, r := range text {
		// Keep alphanumeric and Chinese characters
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || (r >= 0x4e00 && r <= 0x9fff) {
			sb.WriteRune(r)
		}
	}
	return strings.TrimSpace(sb.String())
}

func parseOptionsMap(optionsStr string) map[string]string {
	res := make(map[string]string)
	lines := strings.Split(optionsStr, "\n")
	lineCount := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lineCount++
		letter, coreText := extractOptionPrefixAndText(line)
		if letter != "" {
			res[letter] = coreText
		}
	}
	// Safeguard: if options have multiple lines but we only parsed 1 or 0 keys,
	// it's likely a false positive (e.g. text starting with letters like "AG").
	if len(res) < 2 && lineCount >= 2 {
		return make(map[string]string) // Treat as no letter prefix found
	}
	return res
}

func normalizeOptions(optionsStr string) string {
	optionsMap := parseOptionsMap(optionsStr)
	var texts []string
	if len(optionsMap) == 0 {
		// No options with prefix letter matched.
		// Split by newline and clean each line directly.
		lines := strings.Split(optionsStr, "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			texts = append(texts, cleanOptionText(line))
		}
	} else {
		for _, text := range optionsMap {
			texts = append(texts, cleanOptionText(text))
		}
	}
	sort.Strings(texts)
	return strings.Join(texts, "|")
}

func (c *SQLiteCache) generateKey(question, questionType, options string) string {
	// Normalize options so that the cache key is independent of option ordering
	normOptions := normalizeOptions(options)
	content := fmt.Sprintf("%s|%s|%s", question, questionType, normOptions)
	hash := md5.Sum([]byte(content))
	return hex.EncodeToString(hash[:])
}

func generateRawKey(question, options string) string {
	content := fmt.Sprintf("%s|%s", question, options)
	hash := md5.Sum([]byte(content))
	return hex.EncodeToString(hash[:])
}

func (c *SQLiteCache) Get(question, questionType, options string) (string, bool) {
	// 1. Try direct lookup with our normalized key
	key := c.generateKey(question, questionType, options)
	var cachedOptions, cachedAnswer string
	query := `SELECT options, answer FROM cache WHERE key = ?`
	err := c.db.QueryRow(query, key).Scan(&cachedOptions, &cachedAnswer)
	if err == nil {
		if options != "" && cachedOptions != "" && (questionType == "single" || questionType == "multiple") {
			mappedAnswer := mapAnswerOptions(cachedAnswer, cachedOptions, options)
			if mappedAnswer != "" {
				return mappedAnswer, true
			}
		}
		return cachedAnswer, true
	}

	// 2. Try raw key lookup (Python style: md5(question + "|" + options))
	rawKey := generateRawKey(question, options)
	err = c.db.QueryRow(query, rawKey).Scan(&cachedOptions, &cachedAnswer)
	if err == nil {
		if options != "" && cachedOptions != "" && (questionType == "single" || questionType == "multiple") {
			mappedAnswer := mapAnswerOptions(cachedAnswer, cachedOptions, options)
			if mappedAnswer != "" {
				return mappedAnswer, true
			}
		}
		return cachedAnswer, true
	}

	// 3. Fallback: Query all candidates with same question to find shuffled matches (e.g. for existing cache db)
	candidatesQuery := `SELECT options, answer FROM cache WHERE question = ?`
	rows, err := c.db.Query(candidatesQuery, question)
	if err == nil {
		defer rows.Close()
		normQueryOpts := normalizeOptions(options)

		for rows.Next() {
			var candOptions, candAnswer string
			if err := rows.Scan(&candOptions, &candAnswer); err == nil {
				normCandOpts := normalizeOptions(candOptions)
				if normCandOpts == normQueryOpts {
					// Found shuffled match!
					if options != "" && candOptions != "" && (questionType == "single" || questionType == "multiple") {
						mappedAnswer := mapAnswerOptions(candAnswer, candOptions, options)
						if mappedAnswer != "" {
							// Write new combination for future direct hits
							c.Set(question, mappedAnswer, questionType, options)
							return mappedAnswer, true
						}
					}
					c.Set(question, candAnswer, questionType, options)
					return candAnswer, true
				}
			}
		}
	}

	return "", false
}

func mapAnswerOptions(cachedAnswer, cachedOptions, queryOptions string) string {
	cachedMap := parseOptionsMap(cachedOptions)
	queryMap := parseOptionsMap(queryOptions)

	// Helper to check if cachedAnswer is letter-based
	isLetterBased := true
	parts := strings.Split(cachedAnswer, "#")
	if len(cachedMap) == 0 {
		isLetterBased = false
	} else {
		for _, part := range parts {
			part = strings.ToUpper(strings.TrimSpace(part))
			if _, found := cachedMap[part]; !found {
				isLetterBased = false
				break
			}
		}
	}

	if isLetterBased {
		// Case A: Letter-based cached answer
		// e.g. cachedAnswer is "A#C"
		if len(queryMap) == 0 {
			// Query has no letters, map cached letter to query text option
			var mappedTexts []string
			for _, part := range parts {
				part = strings.ToUpper(strings.TrimSpace(part))
				if text, found := cachedMap[part]; found {
					// find matching raw option in queryOptions
					matchedRaw := findMatchingRawOption(text, queryOptions)
					if matchedRaw != "" {
						mappedTexts = append(mappedTexts, matchedRaw)
					}
				}
			}
			if len(mappedTexts) > 0 {
				return strings.Join(mappedTexts, "#")
			}
			return cachedAnswer // Fallback
		}

		// Both cached and query have letters, map letter to letter
		queryReverseMap := make(map[string]string)
		for letter, text := range queryMap {
			queryReverseMap[cleanOptionText(text)] = letter
		}

		var mappedParts []string
		for _, part := range parts {
			part = strings.ToUpper(strings.TrimSpace(part))
			cachedText := cachedMap[part]
			queryLetter, found := queryReverseMap[cleanOptionText(cachedText)]
			if !found {
				// Try partial match if exact clean text fails
				queryLetter = findBestMatchingLetter(cachedText, queryMap)
			}
			if queryLetter != "" {
				mappedParts = append(mappedParts, queryLetter)
			}
		}
		if len(mappedParts) == len(parts) {
			sort.Strings(mappedParts)
			return strings.Join(mappedParts, "#")
		}
		return cachedAnswer // Fallback
	}

	// Case B: Text-based cached answer
	// e.g. cachedAnswer is "高热" or "纤维蛋白单体含量"
	if len(queryMap) > 0 {
		// Query has letters, map text to query letters
		var mappedParts []string
		for _, part := range parts {
			part = strings.TrimSpace(part)
			queryLetter := findBestMatchingLetter(part, queryMap)
			if queryLetter != "" {
				mappedParts = append(mappedParts, queryLetter)
			}
		}
		if len(mappedParts) > 0 {
			sort.Strings(mappedParts)
			return strings.Join(mappedParts, "#")
		}
	} else {
		// Neither has letters, map text to query text
		var mappedTexts []string
		for _, part := range parts {
			part = strings.TrimSpace(part)
			matchedRaw := findMatchingRawOption(part, queryOptions)
			if matchedRaw != "" {
				mappedTexts = append(mappedTexts, matchedRaw)
			}
		}
		if len(mappedTexts) > 0 {
			return strings.Join(mappedTexts, "#")
		}
	}

	return cachedAnswer
}

// Find raw line in queryOptions that matches/contains the text
func findMatchingRawOption(text string, queryOptions string) string {
	cleanText := cleanOptionText(text)
	if cleanText == "" {
		return ""
	}
	lines := strings.Split(queryOptions, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		cleanLine := cleanOptionText(line)
		if strings.Contains(cleanLine, cleanText) || strings.Contains(cleanText, cleanLine) {
			return line
		}
	}
	return ""
}

// Find query option letter that best matches the text
func findBestMatchingLetter(text string, queryMap map[string]string) string {
	cleanText := cleanOptionText(text)
	if cleanText == "" {
		return ""
	}
	for letter, optText := range queryMap {
		cleanOpt := cleanOptionText(optText)
		if strings.Contains(cleanOpt, cleanText) || strings.Contains(cleanText, cleanOpt) {
			return letter
		}
	}
	return ""
}

func (c *SQLiteCache) Set(question, answer, questionType, options string) {
	key := c.generateKey(question, questionType, options)
	query := `INSERT OR REPLACE INTO cache (key, question, question_type, options, answer, timestamp) VALUES (?, ?, ?, ?, ?, ?)`
	_, err := c.db.Exec(query, key, question, questionType, options, answer, time.Now().Unix())
	if err != nil {
		logError("保存缓存到数据库失败: %v", err)
	}
}

func (c *SQLiteCache) Clear() {
	query := `DELETE FROM cache`
	_, err := c.db.Exec(query)
	if err != nil {
		logError("清空数据库缓存失败: %v", err)
	}
}

func (c *SQLiteCache) Len() int {
	var count int
	query := `SELECT COUNT(*) FROM cache`
	err := c.db.QueryRow(query).Scan(&count)
	if err != nil {
		logError("获取缓存总数失败: %v", err)
		return 0
	}
	return count
}
