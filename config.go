package main

import (
	"bufio"
	"flag"
	"os"
	"strconv"
	"strings"
)

type ConfigStruct struct {
	Host            string
	Port            int
	Debug           bool
	OpenAIApiKey    string
	OpenAIModel     string
	OpenAIApiBase   string
	LogLevel        string
	AccessToken     string
	MaxTokens       int
	Temperature     float64
	EnableCache     bool
	CacheExpiration int
	Proxy           string // Custom HTTP proxy URL
}

var Config ConfigStruct

func loadDotEnv() {
	file, err := os.Open(".env")
	if err != nil {
		return // .env is optional, system env vars take precedence
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])
			
			// Strip inline comments if any
			if idx := strings.Index(val, " #"); idx != -1 {
				val = strings.TrimSpace(val[:idx])
			} else if idx := strings.Index(val, "\t#"); idx != -1 {
				val = strings.TrimSpace(val[:idx])
			}
			
			// Strip quotes if any
			if (strings.HasPrefix(val, "\"") && strings.HasSuffix(val, "\"")) ||
				(strings.HasPrefix(val, "'") && strings.HasSuffix(val, "'")) {
				val = val[1 : len(val)-1]
			}
			
			// Set as env variable if not already set by system
			if os.Getenv(key) == "" {
				os.Setenv(key, val)
			}
		}
	}
}

func initConfig() {
	loadDotEnv()

	Config.Host = getEnv("HOST", "0.0.0.0")
	Config.Port = getEnvInt("PORT", 5000) // Default port 5000
	Config.Debug = getEnvBool("DEBUG", true)

	Config.OpenAIApiKey = getEnv("OPENAI_API_KEY", "")
	Config.OpenAIModel = getEnv("OPENAI_MODEL", "gpt-3.5-turbo")
	Config.OpenAIApiBase = getEnv("OPENAI_API_BASE", "https://api.openai.com/v1")

	Config.LogLevel = getEnv("LOG_LEVEL", "INFO")
	Config.AccessToken = getEnv("ACCESS_TOKEN", "")

	Config.MaxTokens = getEnvInt("MAX_TOKENS", 500)
	Config.Temperature = getEnvFloat("TEMPERATURE", 0.7)

	Config.EnableCache = getEnvBool("ENABLE_CACHE", true)
	Config.CacheExpiration = getEnvInt("CACHE_EXPIRATION", 86400)
	
	Config.Proxy = getEnv("PROXY", "")

	// Parse flags to override configs
	parseFlags()
}

func parseFlags() {
	flagHost := flag.String("host", Config.Host, "Server bind host")
	flagPort := flag.Int("port", Config.Port, "Server bind port")
	flagApiKey := flag.String("api-key", Config.OpenAIApiKey, "OpenAI API key")
	flagApiBase := flag.String("api-base", Config.OpenAIApiBase, "OpenAI API base URL")
	flagModel := flag.String("model", Config.OpenAIModel, "OpenAI model name")
	flagProxy := flag.String("proxy", Config.Proxy, "Custom HTTP proxy URL")
	flagLogLevel := flag.String("log-level", Config.LogLevel, "Logging level (DEBUG, INFO, WARNING, ERROR, CRITICAL)")
	flagAccessToken := flag.String("token", Config.AccessToken, "Access token for authorization")

	flag.Parse()

	Config.Host = *flagHost
	Config.Port = *flagPort
	Config.OpenAIApiKey = *flagApiKey
	Config.OpenAIApiBase = *flagApiBase
	Config.OpenAIModel = *flagModel
	Config.Proxy = *flagProxy
	Config.LogLevel = *flagLogLevel
	Config.AccessToken = *flagAccessToken
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

func getEnvInt(key string, defaultVal int) int {
	if val := os.Getenv(key); val != "" {
		if idx, err := strconv.Atoi(val); err == nil {
			return idx
		}
	}
	return defaultVal
}

func getEnvFloat(key string, defaultVal float64) float64 {
	if val := os.Getenv(key); val != "" {
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			return f
		}
	}
	return defaultVal
}

func getEnvBool(key string, defaultVal bool) bool {
	if val := os.Getenv(key); val != "" {
		return strings.ToLower(val) == "true"
	}
	return defaultVal
}
