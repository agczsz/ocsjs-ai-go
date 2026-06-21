package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
)

//go:embed templates/* static/* api_docs.md
var embedFS embed.FS

type QARecord struct {
	Time      string `json:"time"`
	Timestamp string `json:"timestamp"`
	Question  string `json:"question"`
	Type      string `json:"type"`
	Options   string `json:"options"`
	Answer    string `json:"answer"`
}

type DashboardData struct {
	Version      string
	CacheEnabled bool
	CacheSize    int
	Model        string
	Uptime       string
	Records      []QARecord
}

var (
	cache      *SimpleCache
	qaRecords  = []QARecord{}
	recordsMu  sync.Mutex
	startTime  time.Time
	tmpl       *template.Template
	httpClient *http.Client
)

func initTemplates() {
	funcMap := template.FuncMap{
		"tojson": func(v interface{}) template.JS {
			b, _ := json.Marshal(v)
			return template.JS(b)
		},
	}
	var err error
	tmpl, err = template.New("").Funcs(funcMap).ParseFS(embedFS, "templates/*.html")
	if err != nil {
		log.Fatalf("Error parsing templates: %v", err)
	}
}

func initHTTPClient() {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
	}

	if Config.Proxy != "" {
		proxyURL, err := url.Parse(Config.Proxy)
		if err == nil {
			transport.Proxy = http.ProxyURL(proxyURL)
			log.Printf("Using proxy: %s", Config.Proxy)
		} else {
			log.Printf("Failed to parse proxy URL %s: %v", Config.Proxy, err)
		}
	}

	httpClient = &http.Client{
		Transport: transport,
		Timeout:   60 * time.Second,
	}
}

func verifyAccessToken(r *http.Request) bool {
	if Config.AccessToken == "" {
		return true
	}
	token := r.Header.Get("X-Access-Token")
	if token == "" {
		token = r.URL.Query().Get("token")
	}
	if token == "" {
		token = r.FormValue("token")
	}
	return token == Config.AccessToken
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func renderTemplate(w http.ResponseWriter, name string, data interface{}) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	err := tmpl.ExecuteTemplate(w, name, data)
	if err != nil {
		logError("Error rendering template %s: %v", name, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, accept, origin, Cache-Control, X-Requested-With, X-Access-Token")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type OpenAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type OpenAIRequest struct {
	Model       string          `json:"model"`
	Messages    []OpenAIMessage `json:"messages"`
	Temperature float64         `json:"temperature"`
	MaxTokens   int             `json:"max_tokens"`
}

type OpenAIChoice struct {
	Message OpenAIMessage `json:"message"`
}

type OpenAIResponse struct {
	Choices []OpenAIChoice `json:"choices"`
}

func callOpenAI(prompt string) (string, error) {
	reqBody := OpenAIRequest{
		Model:       Config.OpenAIModel,
		Temperature: Config.Temperature,
		MaxTokens:   Config.MaxTokens,
		Messages: []OpenAIMessage{
			{
				Role:    "system",
				Content: "你是一个专业的考试答题助手。请直接回答答案，不要解释。选择题只回答选项的内容(如：地球)；多选题用#号分隔答案,只回答选项的内容(如中国#世界#地球)；判断题只回答: 正确/对/true/√ 或 错误/错/false/×；填空题直接给出答案。",
			},
			{
				Role:    "user",
				Content: prompt,
			},
		},
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	apiURL := strings.TrimSuffix(Config.OpenAIApiBase, "/") + "/chat/completions"
	req, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+Config.OpenAIApiKey)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API error: status code %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	var respBody OpenAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&respBody); err != nil {
		return "", err
	}

	if len(respBody.Choices) == 0 {
		return "", fmt.Errorf("empty choices from API response")
	}

	return respBody.Choices[0].Message.Content, nil
}

func handleSearch(w http.ResponseWriter, r *http.Request) {
	if !verifyAccessToken(r) {
		writeJSON(w, http.StatusForbidden, map[string]interface{}{
			"code": 0,
			"msg":  "无效的访问令牌",
		})
		return
	}

	startTimeSearch := time.Now()
	var question, questionType, options string

	if r.Method == "GET" {
		question = r.URL.Query().Get("title")
		questionType = r.URL.Query().Get("type")
		options = r.URL.Query().Get("options")
	} else if r.Method == "POST" {
		contentType := r.Header.Get("Content-Type")
		if strings.Contains(contentType, "application/json") {
			var data struct {
				Title   string `json:"title"`
				Type    string `json:"type"`
				Options string `json:"options"`
			}
			if err := json.NewDecoder(r.Body).Decode(&data); err == nil {
				question = data.Title
				questionType = data.Type
				options = data.Options
			}
		} else {
			r.ParseMultipartForm(32 << 20)
			question = r.FormValue("title")
			questionType = r.FormValue("type")
			options = r.FormValue("options")
		}
	} else {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	logInfo("接收到问题: '%s...' (类型: %s)", truncateString(question, 50), questionType)

	if question == "" {
		logWarning("未提供问题内容")
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"code": 0,
			"msg":  "未提供问题内容",
		})
		return
	}

	if Config.EnableCache && cache != nil {
		if cachedAnswer, found := cache.Get(question, questionType, options); found {
			elapsed := time.Since(startTimeSearch).Seconds()
			logInfo("从缓存获取答案 (耗时: %.2f秒)", elapsed)
			writeJSON(w, http.StatusOK, formatAnswerForOCS(question, cachedAnswer))
			return
		}
	}

	prompt := parseQuestionAndOptions(question, options, questionType)
	aiAnswer, err := callOpenAI(prompt)
	if err != nil {
		logError("处理问题时发生错误: %v", err)
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"code": 0,
			"msg":  fmt.Sprintf("发生错误: %v", err),
		})
		return
	}

	processedAnswer := extractAnswer(aiAnswer, questionType)

	if Config.EnableCache && cache != nil {
		cache.Set(question, processedAnswer, questionType, options)
	}

	now := time.Now()
	record := QARecord{
		Time:      now.Format("2006-01-02 15:04:05"),
		Timestamp: now.Format(time.RFC3339),
		Question:  question,
		Type:      questionType,
		Options:   options,
		Answer:    processedAnswer,
	}

	recordsMu.Lock()
	qaRecords = append(qaRecords, record)
	if len(qaRecords) > 100 {
		copy(qaRecords, qaRecords[1:])
		qaRecords = qaRecords[:len(qaRecords)-1]
	}
	recordsMu.Unlock()

	elapsed := time.Since(startTimeSearch).Seconds()
	logInfo("问题处理完成 (耗时: %.2f秒)", elapsed)

	writeJSON(w, http.StatusOK, formatAnswerForOCS(question, processedAnswer))
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":        "ok",
		"message":       "AI题库服务运行正常",
		"version":       "1.0.0",
		"cache_enabled": Config.EnableCache,
		"model":         Config.OpenAIModel,
	})
}

func handleClearCache(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if !verifyAccessToken(r) {
		writeJSON(w, http.StatusForbidden, map[string]interface{}{
			"success": false,
			"message": "无效的访问令牌",
		})
		return
	}
	if !Config.EnableCache || cache == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": false,
			"message": "缓存未启用",
		})
		return
	}
	cache.Clear()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": "缓存已清除",
	})
}

func handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if !verifyAccessToken(r) {
		writeJSON(w, http.StatusForbidden, map[string]interface{}{
			"success": false,
			"message": "无效的访问令牌",
		})
		return
	}

	cacheSize := 0
	if Config.EnableCache && cache != nil {
		cacheSize = cache.Len()
	}

	recordsMu.Lock()
	recordsCount := len(qaRecords)
	recordsMu.Unlock()

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"version":          "1.0.0",
		"uptime":           time.Since(startTime).Seconds(),
		"model":            Config.OpenAIModel,
		"cache_enabled":    Config.EnableCache,
		"cache_size":       cacheSize,
		"qa_records_count": recordsCount,
	})
}

func handleDocs(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	content, err := embedFS.ReadFile("api_docs.md")
	if err != nil {
		http.Error(w, "Documentation file not found", http.StatusNotFound)
		return
	}

	var buf bytes.Buffer
	md := goldmark.New(
		goldmark.WithExtensions(extension.Table),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	)
	if err := md.Convert(content, &buf); err != nil {
		buf.Reset()
		buf.WriteString(fmt.Sprintf("<pre>%s</pre>", string(content)))
	}

	htmlContent := buf.String()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `
<html>
	<head>
		<title>AI题库服务 - API文档</title>
		<style>
			body { font-family: Arial, sans-serif; margin: 40px; line-height: 1.6; }
			h1, h2, h3 { color: #2c3e50; }
			.container { max-width: 800px; margin: 0 auto; }
			code { background: #e0e0e0; padding: 2px 4px; border-radius: 3px; }
			pre { background: #f4f4f4; padding: 10px; border-radius: 4px; overflow-x: auto; }
			table { border-collapse: collapse; width: 100%%; }
			th, td { border: 1px solid #ddd; padding: 8px; }
			th { background-color: #f4f4f4; }
		</style>
	</head>
	<body>
		<div class="container">
			%s
		</div>
	</body>
</html>
`, htmlContent)
}

func main() {
	startTime = time.Now()

	// Initialize Config
	initConfig()

	// Initialize Logger
	setupLogger()

	logInfo("正在启动 AI题库服务...")

	if Config.OpenAIApiKey == "" {
		logCritical("未设置OpenAI API密钥，请在.env文件中配置OPENAI_API_KEY")
		os.Exit(1)
	}

	// Initialize HTTP Client
	initHTTPClient()

	// Initialize Cache
	if Config.EnableCache {
		cache = NewSimpleCache(Config.CacheExpiration)
		logInfo("缓存功能已启用，已从 cache.json 加载持久化缓存，失效时间: %d秒", Config.CacheExpiration)
	} else {
		logInfo("缓存功能未启用")
	}

	// Initialize Templates
	initTemplates()

	// Setup Server Mux
	mux := http.NewServeMux()

	// Routes
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		renderTemplate(w, "index.html", nil)
	})

	mux.HandleFunc("/dashboard", func(w http.ResponseWriter, r *http.Request) {
		uptimeSeconds := int(time.Since(startTime).Seconds())
		days := uptimeSeconds / 86400
		hours := (uptimeSeconds % 86400) / 3600
		minutes := (uptimeSeconds % 3600) / 60
		uptimeStr := fmt.Sprintf("%d天%d小时%d分钟", days, hours, minutes)

		recordsMu.Lock()
		recordsCopy := make([]QARecord, len(qaRecords))
		copy(recordsCopy, qaRecords)
		recordsMu.Unlock()

		cacheSize := 0
		if Config.EnableCache && cache != nil {
			cacheSize = cache.Len()
		}

		data := DashboardData{
			Version:      "1.1.0",
			CacheEnabled: Config.EnableCache,
			CacheSize:    cacheSize,
			Model:        Config.OpenAIModel,
			Uptime:       uptimeStr,
			Records:      recordsCopy,
		}
		renderTemplate(w, "dashboard.html", data)
	})

	mux.HandleFunc("/docs", handleDocs)
	mux.HandleFunc("/api/search", handleSearch)
	mux.HandleFunc("/api/health", handleHealth)
	mux.HandleFunc("/api/cache/clear", handleClearCache)
	mux.HandleFunc("/api/stats", handleStats)

	// Serve static files
	mux.Handle("/static/", http.FileServer(http.FS(embedFS)))

	// Start server
	addr := fmt.Sprintf("%s:%d", Config.Host, Config.Port)
	logInfo("服务运行于: http://%s", addr)

	// Apply CORS
	serverHandler := corsMiddleware(mux)

	server := &http.Server{
		Addr:    addr,
		Handler: serverHandler,
	}

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logCritical("服务启动失败: %v", err)
	}
}

