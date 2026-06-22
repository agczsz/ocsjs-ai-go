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

	if options != "" {
		sb.WriteString(fmt.Sprintf("\n选项:\n%s\n", options))
	}

	if questionType == "single" {
		sb.WriteString("\n这是一道单选题，请仅返回正确答案的选项字母（如A、B、C、D），不要有其他解释，包括答案是等描述。")
	} else if questionType == "multiple" {
		sb.WriteString("\n这是一道多选题，请返回所有正确答案的选项字母，用#号分隔（如A#C#D），不要有其他解释。包括答案是等描述。")
	} else if questionType == "judgement" {
		sb.WriteString("\n这是一道判断题，请仅返回\"正确\"或\"错误\"，不要有其他解释。包括答案是等描述。")
	} else if questionType == "completion" {
		sb.WriteString("\n这是一道填空题，请直接给出填空答案，如果有多个空，用#号分隔。")
	} else {
		sb.WriteString("\n请直接给出答案。")
	}

	return sb.String()
}

// extractAnswer cleans and extracts answer based on type
func extractAnswer(aiResponse, questionType string) string {
	trimmed := strings.TrimSpace(aiResponse)

	// Strip reasoning model thinking blocks. Models like DeepSeek-R1,
	// MiniMax-M3, and Qwen-QwQ wrap their chain-of-thought in <think>...</think>
	// tags before the final answer. Without stripping, the OCS client sees the
	// full reasoning text instead of just the answer.
	if idx := strings.LastIndex(trimmed, "</think>"); idx != -1 {
		trimmed = strings.TrimSpace(trimmed[idx+len("</think>"):])
	}

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
