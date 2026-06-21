package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	logMu      sync.Mutex
	currentDay string
	logFile    *os.File
	multiW     io.Writer
)

// LogLevel maps configuration log levels
const (
	LevelDebug    = 0
	LevelInfo     = 1
	LevelWarning  = 2
	LevelError    = 3
	LevelCritical = 4
)

var levelMap = map[string]int{
	"DEBUG":    LevelDebug,
	"INFO":     LevelInfo,
	"WARNING":  LevelWarning,
	"ERROR":    LevelError,
	"CRITICAL": LevelCritical,
}

func getLogLevel() int {
	if lvl, exists := levelMap[strings.ToUpper(Config.LogLevel)]; exists {
		return lvl
	}
	return LevelInfo
}

// setupLogger sets up log output to stdout and daily log file
func setupLogger() {
	logMu.Lock()
	defer logMu.Unlock()

	// Create logs folder
	os.MkdirAll("logs", 0755)

	today := time.Now().Format("2006-01-02")
	if today != currentDay {
		if logFile != nil {
			logFile.Close()
		}

		currentDay = today
		fileName := filepath.Join("logs", fmt.Sprintf("ai_answer_service_%s.log", today))
		var err error
		logFile, err = os.OpenFile(fileName, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			log.Printf("Failed to open log file %s: %v", fileName, err)
			multiW = os.Stdout
		} else {
			multiW = io.MultiWriter(os.Stdout, logFile)
		}
		
		log.SetOutput(multiW)
		log.SetFlags(log.Ldate | log.Ltime)
	}
}

// Check daily rotation before logging
func checkLogRotation() {
	today := time.Now().Format("2006-01-02")
	if today != currentDay {
		setupLogger()
	}
}

func logDebug(format string, v ...interface{}) {
	if getLogLevel() <= LevelDebug {
		checkLogRotation()
		log.Printf("[DEBUG] "+format, v...)
	}
}

func logInfo(format string, v ...interface{}) {
	if getLogLevel() <= LevelInfo {
		checkLogRotation()
		log.Printf("[INFO] "+format, v...)
	}
}

func logWarning(format string, v ...interface{}) {
	if getLogLevel() <= LevelWarning {
		checkLogRotation()
		log.Printf("[WARNING] "+format, v...)
	}
}

func logError(format string, v ...interface{}) {
	if getLogLevel() <= LevelError {
		checkLogRotation()
		log.Printf("[ERROR] "+format, v...)
	}
}

func logCritical(format string, v ...interface{}) {
	if getLogLevel() <= LevelCritical {
		checkLogRotation()
		log.Printf("[CRITICAL] "+format, v...)
	}
}

// formatAnswerForOCS formats response into OCS spec
func formatAnswerForOCS(question, answer string) map[string]interface{} {
	return map[string]interface{}{
		"code":     1,
		"question": question,
		"answer":   answer,
	}
}

// parseQuestionAndOptions builds the OpenAI prompt
func parseQuestionAndOptions(question, options, questionType string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("问题: %s\n", question))

	typePrompts := map[string]string{
		"single":     "这是一道单选题。",
		"multiple":   "这是一道多选题，答案请用#符号分隔。",
		"judgement":  "这是一道判断题，需要回答：正确/对/true/对/错/false/×。",
		"completion": "这是一道填空题。",
	}

	if desc, ok := typePrompts[questionType]; ok {
		if questionType == "judgement" {
			sb.WriteString("这是一道判断题，需要回答：正确/对/true/√ 或者 错误/错/false/×。\n")
		} else {
			sb.WriteString(desc + "\n")
		}
	}

	if options != "" {
		sb.WriteString(fmt.Sprintf("选项:\n%s\n", options))
	}

	sb.WriteString("请直接给出答案，不要解释。")
	return sb.String()
}

// extractAnswer cleans and extracts answer based on type
func extractAnswer(aiResponse, questionType string) string {
	trimmed := strings.TrimSpace(aiResponse)
	if questionType == "multiple" {
		lines := strings.Split(trimmed, "\n")
		for _, line := range lines {
			upperLine := strings.ToUpper(line)
			hasOptions := false
			for _, opt := range []string{"A", "B", "C", "D"} {
				if strings.Contains(upperLine, opt) {
					hasOptions = true
					break
				}
			}
			if hasOptions && !strings.Contains(line, "#") {
				var opts []string
				for _, opt := range []string{"A", "B", "C", "D", "E", "F"} {
					if strings.Contains(upperLine, opt) {
						opts = append(opts, opt)
					}
				}
				if len(opts) > 0 {
					return strings.Join(opts, "#")
				}
			}
		}
	}
	return trimmed
}

func truncateString(s string, l int) string {
	r := []rune(s)
	if len(r) > l {
		return string(r[:l])
	}
	return s
}
