package main

import (
	"bufio"
	"flag"
	"os"
	"strconv"
	"strings"
)

type ConfigStruct struct {
	Host               string
	Port               int
	Debug              bool
	OpenAIApiKey       string
	OpenAIModel        string
	OpenAIApiBase      string
	LogLevel           string
	AccessToken        string
	MaxTokens          int
	Temperature        float64
	EnableCache        bool
	CacheExpiration    int
	Proxy              string  // Custom HTTP proxy URL
	MultiModelMode     string  // standard, confidence, fallback, search
	ConfidenceThreshold float64 // default 0.7
	LLMTimeout         int     // LLM request timeout in seconds
	EvalTimeout        int     // Confidence eval / search timeout in seconds
	ExaApiKey          string  // Exa AI API key
	ExaBaseUrl         string  // Exa AI API base url
	ApiBase1           string  // Fallback API Base 1
	ApiKey1            string  // Fallback API Key 1
	Model1             string  // Fallback model 1
	ApiBase2           string  // Fallback API Base 2
	ApiKey2            string  // Fallback API Key 2
	Model2             string  // Fallback model 2
	ApiBase3           string  // Fallback API Base 3
	ApiKey3            string  // Fallback API Key 3
	Model3             string  // Fallback model 3
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
	Config.Port = getEnvInt("PORT", 5000)
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
	
	// Multi-model and search configuration
	Config.MultiModelMode = getEnv("MULTI_MODEL_MODE", "standard")
	Config.ConfidenceThreshold = getEnvFloat("CONFIDENCE_THRESHOLD", 0.7)
	Config.LLMTimeout = getEnvInt("LLM_TIMEOUT", 240)
	Config.EvalTimeout = getEnvInt("EVAL_TIMEOUT", 30)
	Config.ExaApiKey = getEnv("EXA_API_KEY", "")
	Config.ExaBaseUrl = getEnv("EXA_BASE_URL", "https://api.exa.ai")
	
	Config.ApiBase1 = getEnv("API_BASE1", "")
	Config.ApiKey1 = getEnv("API_KEY1", "")
	Config.Model1 = getEnv("MODEL1", "")
	Config.ApiBase2 = getEnv("API_BASE2", "")
	Config.ApiKey2 = getEnv("API_KEY2", "")
	Config.Model2 = getEnv("MODEL2", "")
	Config.ApiBase3 = getEnv("API_BASE3", "")
	Config.ApiKey3 = getEnv("API_KEY3", "")
	Config.Model3 = getEnv("MODEL3", "")

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
	
	flagMultiMode := flag.String("multi-mode", Config.MultiModelMode, "Multi-model mode: standard, confidence, fallback, search")
	flagThreshold := flag.Float64("threshold", Config.ConfidenceThreshold, "Confidence threshold for scoring mode")
	flagExaKey := flag.String("exa-key", Config.ExaApiKey, "Exa AI API key")
	flagApiBase1 := flag.String("api-base1", Config.ApiBase1, "Fallback model 1 API base URL")
	flagApiKey1 := flag.String("api-key1", Config.ApiKey1, "Fallback model 1 API key")
	flagModel1 := flag.String("model1", Config.Model1, "Fallback model 1")
	flagApiBase2 := flag.String("api-base2", Config.ApiBase2, "Fallback model 2 API base URL")
	flagApiKey2 := flag.String("api-key2", Config.ApiKey2, "Fallback model 2 API key")
	flagModel2 := flag.String("model2", Config.Model2, "Fallback model 2")
	flagApiBase3 := flag.String("api-base3", Config.ApiBase3, "Fallback model 3 API base URL")
	flagApiKey3 := flag.String("api-key3", Config.ApiKey3, "Fallback model 3 API key")
	flagModel3 := flag.String("model3", Config.Model3, "Fallback model 3")

	flag.Parse()

	Config.Host = *flagHost
	Config.Port = *flagPort
	Config.OpenAIApiKey = *flagApiKey
	Config.OpenAIApiBase = *flagApiBase
	Config.OpenAIModel = *flagModel
	Config.Proxy = *flagProxy
	Config.LogLevel = *flagLogLevel
	Config.AccessToken = *flagAccessToken
	
	Config.MultiModelMode = *flagMultiMode
	Config.ConfidenceThreshold = *flagThreshold
	Config.ExaApiKey = *flagExaKey
	Config.ApiBase1 = *flagApiBase1
	Config.ApiKey1 = *flagApiKey1
	Config.Model1 = *flagModel1
	Config.ApiBase2 = *flagApiBase2
	Config.ApiKey2 = *flagApiKey2
	Config.Model2 = *flagModel2
	Config.ApiBase3 = *flagApiBase3
	Config.ApiKey3 = *flagApiKey3
	Config.Model3 = *flagModel3
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
