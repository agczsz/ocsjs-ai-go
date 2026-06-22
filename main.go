package main

import (
	"bytes"
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	
	_ "modernc.org/sqlite"
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
	cache      *SQLiteCache
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

func validateAnswer(answer, questionType string) bool {
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return false
	}
	switch questionType {
	case "single":
		return len(answer) == 1 && ((answer[0] >= 'A' && answer[0] <= 'Z') || (answer[0] >= 'a' && answer[0] <= 'z'))
	case "multiple":
		parts := strings.Split(answer, "#")
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if len(p) != 1 {
				return false
			}
			r := p[0]
			if !((r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')) {
				return false
			}
		}
		return true
	case "judgement":
		return answer == "正确" || answer == "错误"
	case "completion":
		return len(answer) > 0
	default:
		return len(answer) > 0
	}
}

func cleanAndValidateAnswer(aiResponse, questionType string) (string, bool) {
	// Strip <think>...</think> block if present
	thinkReg := regexp.MustCompile(`(?s)<think>.*?</think>`)
	aiResponse = thinkReg.ReplaceAllString(aiResponse, "")
	aiResponse = strings.TrimSpace(aiResponse)

	ans := extractAnswer(aiResponse, questionType)
	ans = strings.TrimSpace(ans)
	
	if questionType == "single" {
		if len(ans) > 1 {
			reg := regexp.MustCompile(`(?i)\b([A-Z])\b`)
			match := reg.FindStringSubmatch(ans)
			if len(match) == 2 {
				ans = strings.ToUpper(match[1])
			} else {
				regAny := regexp.MustCompile(`(?i)([A-Z])`)
				matchAny := regAny.FindStringSubmatch(ans)
				if len(matchAny) == 2 {
					ans = strings.ToUpper(matchAny[1])
				}
			}
		} else {
			ans = strings.ToUpper(ans)
		}
	}
	
	if questionType == "multiple" {
		parts := strings.Split(ans, "#")
		var cleanParts []string
		for _, p := range parts {
			p = strings.ToUpper(strings.TrimSpace(p))
			if len(p) == 1 && p[0] >= 'A' && p[0] <= 'Z' {
				cleanParts = append(cleanParts, p)
			}
		}
		if len(cleanParts) > 0 {
			sort.Strings(cleanParts)
			ans = strings.Join(cleanParts, "#")
		}
	}

	if questionType == "judgement" {
		if strings.Contains(ans, "对") || strings.Contains(ans, "正确") || strings.Contains(strings.ToLower(ans), "true") || strings.Contains(ans, "√") {
			ans = "正确"
		} else if strings.Contains(ans, "错") || strings.Contains(ans, "错误") || strings.Contains(strings.ToLower(ans), "false") || strings.Contains(ans, "×") {
			ans = "错误"
		}
	}

	return ans, validateAnswer(ans, questionType)
}

func callOpenAIWithModel(apiBase, apiKey, prompt, model string) (string, error) {
	reqBody := OpenAIRequest{
		Model:       model,
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

	apiURL := strings.TrimSuffix(apiBase, "/") + "/chat/completions"
	for attempt := 0; attempt < 3; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(Config.LLMTimeout)*time.Second)
		req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(jsonData))
		if err != nil {
			cancel()
			return "", err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+apiKey)

		resp, err := httpClient.Do(req)
		if err != nil {
			cancel()
			// Retry on transient network errors (EOF, timeout, connection reset)
			if attempt < 2 {
				time.Sleep(time.Duration(1<<attempt) * time.Second)
				continue
			}
			return "", err
		}

		if resp.StatusCode != http.StatusOK {
			bodyBytes, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			cancel()
			return "", fmt.Errorf("API error: status code %d, body: %s", resp.StatusCode, string(bodyBytes))
		}

		var respBody OpenAIResponse
		err = json.NewDecoder(resp.Body).Decode(&respBody)
		resp.Body.Close()
		cancel()
		if err != nil {
			if attempt < 2 {
				time.Sleep(time.Duration(1<<attempt) * time.Second)
				continue
			}
			return "", err
		}

		if len(respBody.Choices) == 0 {
			return "", fmt.Errorf("empty choices from API response")
		}

		return respBody.Choices[0].Message.Content, nil
	}

	return "", fmt.Errorf("max retries exceeded")
}

func callOpenAIConfidence(answer, question, options string) (float64, error) {
	confidencePrompt := fmt.Sprintf(`题目：%s

选项：
%s

我给出的答案是：%s

请评估这个答案正确的可能性有多大，给出0到1之间的一个数字（0表示完全不可能正确，1表示完全确定正确）。只返回数字，不要有其他解释描述。`, question, options, answer)

	reqBody := OpenAIRequest{
		Model:       Config.OpenAIModel,
		Temperature: 0.3,
		MaxTokens:   10,
		Messages: []OpenAIMessage{
			{
				Role:    "system",
				Content: "你是一个专业的答案评估助手，只返回0到1之间的数字。",
			},
			{
				Role:    "user",
				Content: confidencePrompt,
			},
		},
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return 0.5, err
	}

	apiURL := strings.TrimSuffix(Config.OpenAIApiBase, "/") + "/chat/completions"
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(Config.EvalTimeout)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return 0.5, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+Config.OpenAIApiKey)

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0.5, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0.5, fmt.Errorf("status code %d", resp.StatusCode)
	}

	var respBody OpenAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&respBody); err != nil {
		return 0.5, err
	}

	if len(respBody.Choices) == 0 {
		return 0.5, fmt.Errorf("empty choices")
	}

	valStr := strings.TrimSpace(respBody.Choices[0].Message.Content)
	thinkReg := regexp.MustCompile(`(?s)<think>.*?</think>`)
	valStr = thinkReg.ReplaceAllString(valStr, "")
	valStr = strings.TrimSpace(valStr)

	val, err := strconv.ParseFloat(valStr, 64)
	if err != nil {
		reg := regexp.MustCompile(`0\.\d+|1\.0|0|1`)
		match := reg.FindString(valStr)
		if match != "" {
			val, _ = strconv.ParseFloat(match, 64)
		} else {
			return 0.5, fmt.Errorf("failed to parse float from: %s", valStr)
		}
	}

	if val < 0 {
		val = 0
	}
	if val > 1 {
		val = 1
	}
	return val, nil
}

func callOpenAIConfidenceWithModel(apiBase, apiKey, model, answer, question, options string) (float64, error) {
	confidencePrompt := fmt.Sprintf(`题目：%s

选项：
%s

我给出的答案是：%s

请评估这个答案正确的可能性有多大，给出0到1之间的一个数字（0表示完全不可能正确，1表示完全确定正确）。只返回数字，不要有其他解释描述。`, question, options, answer)

	reqBody := OpenAIRequest{
		Model:       model,
		Temperature: 0.3,
		MaxTokens:   10,
		Messages: []OpenAIMessage{
			{
				Role:    "system",
				Content: "你是一个专业的答案评估助手，只返回0到1之间的数字。",
			},
			{
				Role:    "user",
				Content: confidencePrompt,
			},
		},
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return 0.5, err
	}

	apiURL := strings.TrimSuffix(apiBase, "/") + "/chat/completions"
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(Config.EvalTimeout)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return 0.5, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0.5, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0.5, fmt.Errorf("status code %d", resp.StatusCode)
	}

	var respBody OpenAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&respBody); err != nil {
		return 0.5, err
	}

	if len(respBody.Choices) == 0 {
		return 0.5, fmt.Errorf("empty choices")
	}

	valStr := strings.TrimSpace(respBody.Choices[0].Message.Content)
	thinkReg := regexp.MustCompile(`(?s)<think>.*?</think>`)
	valStr = thinkReg.ReplaceAllString(valStr, "")
	valStr = strings.TrimSpace(valStr)

	val, err := strconv.ParseFloat(valStr, 64)
	if err != nil {
		reg := regexp.MustCompile(`0\.\d+|1\.0|0|1`)
		match := reg.FindString(valStr)
		if match != "" {
			val, _ = strconv.ParseFloat(match, 64)
		} else {
			return 0.5, fmt.Errorf("failed to parse float from: %s", valStr)
		}
	}

	if val < 0 {
		val = 0
	}
	if val > 1 {
		val = 1
	}
	return val, nil
}

type ExaSearchRequest struct {
	Query         string `json:"query"`
	UseAutoprompt bool   `json:"useAutoprompt"`
	NumResults    int    `json:"numResults"`
	Contents      struct {
		Text       bool `json:"text"`
		Highlights bool `json:"highlights"`
	} `json:"contents"`
}

type ExaSearchResponse struct {
	Results []struct {
		Title      string   `json:"title"`
		URL        string   `json:"url"`
		Highlights []string `json:"highlights"`
	} `json:"results"`
}

var punctuationRegex = regexp.MustCompile(`[，。！？；：""''（）《》【】、·—…\,\.\!\?\;\:\"\'\(\)\[\]\{\}\<\>\-\_\+\=\*\&\^\%\$\#\@\` + "`" + `\~\|\\\/]`)

func removePunctuation(text string) string {
	cleaned := punctuationRegex.ReplaceAllString(text, " ")
	multiSpaceRegex := regexp.MustCompile(`\s+`)
	cleaned = multiSpaceRegex.ReplaceAllString(cleaned, " ")
	return strings.TrimSpace(cleaned)
}

func callExaSearch(searchQuery string) (string, error) {
	reqBody := ExaSearchRequest{
		Query:         searchQuery,
		UseAutoprompt: true,
		NumResults:    3,
	}
	reqBody.Contents.Text = false
	reqBody.Contents.Highlights = true

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	apiURL := "https://api.exa.ai/search"
	if Config.ExaBaseUrl != "" {
		apiURL = strings.TrimSuffix(Config.ExaBaseUrl, "/") + "/search"
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(Config.EvalTimeout)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", Config.ExaApiKey)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("Exa API error: status code %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	var respBody ExaSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&respBody); err != nil {
		return "", err
	}

	var sb strings.Builder
	if len(respBody.Results) == 0 {
		return "未找到相关搜索结果", nil
	}

	for i, result := range respBody.Results {
		sb.WriteString(fmt.Sprintf("【结果 %d】\n标题: %s\n", i+1, result.Title))
		if len(result.Highlights) > 0 {
			sb.WriteString("相关内容:\n")
			for _, hl := range result.Highlights {
				sb.WriteString(fmt.Sprintf("  - %s\n", hl))
			}
		} else {
			sb.WriteString("相关内容: 无高亮内容\n")
		}
	}

	return sb.String(), nil
}

func callLLMWithValidation(apiBase, apiKey, model string, messages []OpenAIMessage, questionType string, maxRetries int, contextDesc string) (string, error) {
	var lastAnswer string
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		reqBody := OpenAIRequest{
			Model:       model,
			Temperature: 0.3,
			MaxTokens:   500,
			Messages:    messages,
		}

		jsonData, err := json.Marshal(reqBody)
		if err != nil {
			lastErr = err
			continue
		}

		apiURL := strings.TrimSuffix(apiBase, "/") + "/chat/completions"
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(Config.LLMTimeout)*time.Second)
		req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewBuffer(jsonData))
		if err != nil {
			cancel()
			lastErr = err
			continue
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+apiKey)

		resp, err := httpClient.Do(req)
		if err != nil {
			cancel()
			lastErr = err
			if attempt < maxRetries-1 {
				time.Sleep(time.Duration(1<<attempt) * time.Second)
			}
			continue
		}

		if resp.StatusCode != http.StatusOK {
			bodyBytes, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			cancel()
			lastErr = fmt.Errorf("status code %d, body: %s", resp.StatusCode, string(bodyBytes))
			if attempt < maxRetries-1 {
				time.Sleep(time.Duration(1<<attempt) * time.Second)
			}
			continue
		}

		var respBody OpenAIResponse
		err = json.NewDecoder(resp.Body).Decode(&respBody)
		resp.Body.Close()
		cancel()
		if err != nil {
			lastErr = err
			continue
		}

		if len(respBody.Choices) == 0 {
			lastErr = fmt.Errorf("empty choices from API response")
			continue
		}

		aiAnswer := respBody.Choices[0].Message.Content
		lastAnswer = aiAnswer

		cleanedAns, isValid := cleanAndValidateAnswer(aiAnswer, questionType)
		if isValid {
			if attempt > 0 {
				logInfo("[%s] 验证重试成功，答案: %s", contextDesc, cleanedAns)
			}
			return cleanedAns, nil
		} else {
			logWarning("[%s] 答案格式不规范 (尝试 %d/%d): %s", contextDesc, attempt+1, maxRetries, strings.TrimSpace(aiAnswer))
			if attempt < maxRetries-1 {
				time.Sleep(1 * time.Second)
			}
		}
	}

	cleanedAns, _ := cleanAndValidateAnswer(lastAnswer, questionType)
	logWarning("[%s] 警告: 所有重试均未能获得有效格式答案，返回最佳处理结果: %s", contextDesc, cleanedAns)
	return cleanedAns, lastErr
}

func answerWithConfidence(title, options, questionType string) (string, error) {
	startTimeConf := time.Now()
	threshold := Config.ConfidenceThreshold

	prompt := parseQuestionAndOptions(title, options, questionType)

	logInfo("[置信度模式] 正在获取初始答案...")
	initialAnswer, err := callLLMWithValidation(
		Config.OpenAIApiBase,
		Config.OpenAIApiKey,
		Config.OpenAIModel,
		[]OpenAIMessage{
			{Role: "system", Content: "你是一个专业的答题助手，请根据题目给出准确答案。"},
			{Role: "user", Content: prompt},
		},
		questionType,
		3,
		"初始答案获取",
	)
	if err != nil {
		return "", err
	}
	logInfo("[初始回答] 答案: %s (耗时: %.2fs)", initialAnswer, time.Since(startTimeConf).Seconds())

	logInfo("[置信度模式] 正在评估答案置信度...")
	confidence, err := callOpenAIConfidence(initialAnswer, title, options)
	if err != nil {
		logWarning("[置信度评估失败] %v, 使用默认置信度 0.5", err)
		confidence = 0.5
	}
	logInfo("[置信度评估] 答案: %s, 置信度: %.2f, 阈值: %.2f", initialAnswer, confidence, threshold)

	if confidence >= threshold {
		logInfo("[置信度充足] 置信度 %.2f >= %.2f, 直接返回答案", confidence, threshold)
		return initialAnswer, nil
	}

	logInfo("[置信度不足] 置信度 %.2f < %.2f", confidence, threshold)

	if Config.ExaApiKey != "" {
		logInfo("[联网搜索模式] 检测到 EXA_API_KEY，开始进行联网搜索...")
		searchQuery := removePunctuation(title)
		if options != "" && (questionType == "single" || questionType == "multiple") {
			searchQuery += " " + removePunctuation(options)
		}

		searchContext, err := callExaSearch(searchQuery)
		if err != nil {
			logWarning("[搜索失败] %v, 回退到无搜索重答模式", err)
		} else {
			logInfo("[搜索完成] 获取到上下文信息，长度: %d 字符", len(searchContext))
			
			enhancedPrompt := fmt.Sprintf(`注意：这是第二次回答此问题。

第一次回答的答案是：%s，置信度评估：%.2f（置信度较低于阈值 %.2f）

由于置信度较低，通过联网搜索获取到以下相关参考信息：

%s

---

%s

请结合搜索信息和首次回答的答案和对应的置信度，重新仔细分析题目，给出更准确的答案。`, initialAnswer, confidence, threshold, searchContext, prompt)

			finalAnswer, err := callLLMWithValidation(
				Config.OpenAIApiBase,
				Config.OpenAIApiKey,
				Config.OpenAIModel,
				[]OpenAIMessage{
					{Role: "system", Content: "你是一个专业的答题助手，请根据题目、第一次答案的参考和联网搜索的信息给出准确答案。"},
					{Role: "user", Content: enhancedPrompt},
				},
				questionType,
				3,
				"基于搜索和首次答案回答",
			)
			if err != nil {
				return initialAnswer, nil
			}
			return finalAnswer, nil
		}
	}

	logInfo("[重新分析模式] 开始附上首次答案重新分析...")
	retryPrompt := fmt.Sprintf(`注意：这是第二次回答此问题。

第一次回答的答案是：%s，置信度评估：%.2f（置信度较低，低于阈值 %.2f）

由于置信度较低，请重新仔细分析题目，给出更准确的答案。

%s`, initialAnswer, confidence, threshold, prompt)

	finalAnswer, err := callLLMWithValidation(
		Config.OpenAIApiBase,
		Config.OpenAIApiKey,
		Config.OpenAIModel,
		[]OpenAIMessage{
			{Role: "system", Content: "你是一个专业的答题助手，请根据题目给出准确答案。注意第一次答案的置信度较低，请重新仔细分析。"},
			{Role: "user", Content: retryPrompt},
		},
		questionType,
		3,
		"置信度低重新分析",
	)
	if err != nil {
		return initialAnswer, nil
	}
	return finalAnswer, nil
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

	var processedAnswer string
	var err error

	// Determine strategy based on MultiModelMode
	if Config.MultiModelMode == "confidence" {
		processedAnswer, err = answerWithConfidence(question, options, questionType)
	} else if Config.MultiModelMode == "fallback" {
		// fallback mode logic
		type FallbackConfig struct {
			ApiBase string
			ApiKey  string
			Model   string
		}
		var fallbackConfigs []FallbackConfig

		if Config.Model1 != "" {
			apiBase := Config.ApiBase1
			if apiBase == "" {
				apiBase = Config.OpenAIApiBase
			}
			apiKey := Config.ApiKey1
			if apiKey == "" {
				apiKey = Config.OpenAIApiKey
			}
			fallbackConfigs = append(fallbackConfigs, FallbackConfig{
				ApiBase: apiBase,
				ApiKey:  apiKey,
				Model:   Config.Model1,
			})
		}
		if Config.Model2 != "" {
			apiBase := Config.ApiBase2
			if apiBase == "" {
				apiBase = Config.OpenAIApiBase
			}
			apiKey := Config.ApiKey2
			if apiKey == "" {
				apiKey = Config.OpenAIApiKey
			}
			fallbackConfigs = append(fallbackConfigs, FallbackConfig{
				ApiBase: apiBase,
				ApiKey:  apiKey,
				Model:   Config.Model2,
			})
		}
		if Config.Model3 != "" {
			apiBase := Config.ApiBase3
			if apiBase == "" {
				apiBase = Config.OpenAIApiBase
			}
			apiKey := Config.ApiKey3
			if apiKey == "" {
				apiKey = Config.OpenAIApiKey
			}
			fallbackConfigs = append(fallbackConfigs, FallbackConfig{
				ApiBase: apiBase,
				ApiKey:  apiKey,
				Model:   Config.Model3,
			})
		}

		// Fallback to standard OpenAIModel if no fallback models configured
		if len(fallbackConfigs) == 0 {
			fallbackConfigs = append(fallbackConfigs, FallbackConfig{
				ApiBase: Config.OpenAIApiBase,
				ApiKey:  Config.OpenAIApiKey,
				Model:   Config.OpenAIModel,
			})
		}

		prompt := parseQuestionAndOptions(question, options, questionType)
		var currentAnswer string

		for idx := 0; idx < len(fallbackConfigs); {
			mCfg := fallbackConfigs[idx]
			logInfo("[Fallback模式] 正在使用模型 %d: %s 获取答案...", idx+1, mCfg.Model)

			var ans string
			var modelErr error

			if idx > 0 && currentAnswer != "" {
				// We have a previous answer that had low confidence, so we query this model with search context/retry
				if Config.ExaApiKey != "" {
					logInfo("[Fallback模式-联网搜索] 结合 Exa 联网搜索，调用模型 %s...", mCfg.Model)
					searchQuery := removePunctuation(question)
					if options != "" && (questionType == "single" || questionType == "multiple") {
						searchQuery += " " + removePunctuation(options)
					}
					searchContext, sErr := callExaSearch(searchQuery)
					if sErr != nil {
						logWarning("[Fallback模式-搜索失败] %v, 使用普通重答模式", sErr)
						retryPrompt := fmt.Sprintf(`注意：这是备选模型回答。上一模型给出的答案是：%s（但置信度被评估为较低）。请重新仔细分析题目，给出更准确的答案。
%s`, currentAnswer, prompt)
						ans, modelErr = callLLMWithValidation(
							mCfg.ApiBase,
							mCfg.ApiKey,
							mCfg.Model,
							[]OpenAIMessage{
								{Role: "system", Content: "你是一个专业的答题助手，请根据题目给出准确答案。"},
								{Role: "user", Content: retryPrompt},
							},
							questionType,
							2,
							fmt.Sprintf("模型 %s 重答", mCfg.Model),
						)
					} else {
						logInfo("[Fallback模式-搜索完成] 获取到上下文信息，长度: %d 字符", len(searchContext))
						enhancedPrompt := fmt.Sprintf(`注意：这是备选模型回答。
上一模型给出的答案是：%s（但置信度被评估为较低）。

我们通过联网搜索获取到以下相关参考信息：
%s

---
%s

请结合搜索信息重新仔细分析题目，给出更准确的答案。`, currentAnswer, searchContext, prompt)

						ans, modelErr = callLLMWithValidation(
							mCfg.ApiBase,
							mCfg.ApiKey,
							mCfg.Model,
							[]OpenAIMessage{
								{Role: "system", Content: "你是一个专业的答题助手，请参考搜索信息给出最准确的答案。"},
								{Role: "user", Content: enhancedPrompt},
							},
							questionType,
							2,
							fmt.Sprintf("模型 %s 搜索重答", mCfg.Model),
						)
					}
				} else {
					logInfo("[Fallback模式-重答] 附带上一模型答案，直接调用模型 %s...", mCfg.Model)
					retryPrompt := fmt.Sprintf(`注意：这是备选模型回答。上一模型给出的答案是：%s（但置信度被评估为较低）。请重新仔细分析题目，给出更准确的答案。
%s`, currentAnswer, prompt)
					ans, modelErr = callLLMWithValidation(
						mCfg.ApiBase,
						mCfg.ApiKey,
						mCfg.Model,
						[]OpenAIMessage{
							{Role: "system", Content: "你是一个专业的答题助手，请根据题目给出准确答案。"},
							{Role: "user", Content: retryPrompt},
						},
						questionType,
						2,
						fmt.Sprintf("模型 %s 重新分析", mCfg.Model),
					)
				}
			} else {
				// Standard query (e.g. Model 1 initial run)
				ans, modelErr = callLLMWithValidation(
					mCfg.ApiBase,
					mCfg.ApiKey,
					mCfg.Model,
					[]OpenAIMessage{
						{Role: "system", Content: "你是一个专业的答题助手，请根据题目给出准确答案。"},
						{Role: "user", Content: prompt},
					},
					questionType,
					2,
					fmt.Sprintf("模型 %s 首次调用", mCfg.Model),
				)
			}

			if modelErr != nil || ans == "" {
				logWarning("[Fallback模式] 模型 %s 调用失败或答案为空: %v。进入下一顺位模型...", mCfg.Model, modelErr)
				idx++
				currentAnswer = ""
				err = modelErr
				continue
			}

			// Model succeeded and returned a formatted answer
			currentAnswer = ans

			// Check if there is a next model to evaluate confidence
			if idx+1 < len(fallbackConfigs) {
				logInfo("[Fallback模式] 正在使用模型 %s 自行评估其答案置信度...", mCfg.Model)
				
				confidence, confErr := callOpenAIConfidenceWithModel(
					mCfg.ApiBase,
					mCfg.ApiKey,
					mCfg.Model,
					currentAnswer,
					question,
					options,
				)
				if confErr != nil {
					logWarning("[Fallback模式-置信度评估失败] %v, 默认置信度 0.5", confErr)
					confidence = 0.5
				}

				logInfo("[Fallback模式-置信度评估] 答案: %s, 置信度: %.2f, 阈值: %.2f", currentAnswer, confidence, Config.ConfidenceThreshold)

				if confidence >= Config.ConfidenceThreshold {
					logInfo("[Fallback模式-置信度充足] %.2f >= %.2f, 接受当前答案", confidence, Config.ConfidenceThreshold)
					processedAnswer = currentAnswer
					err = nil
					break
				} else {
					logInfo("[Fallback模式-置信度不足] %.2f < %.2f, 切换到下一顺位模型...", confidence, Config.ConfidenceThreshold)
					idx++ // Proceed to next model with currentAnswer populated for search
					continue
				}
			} else {
				// Last model succeeded, accept it directly
				logInfo("[Fallback模式] 已到达最后一个模型，直接接受其答案: %s", currentAnswer)
				processedAnswer = currentAnswer
				err = nil
				break
			}
		}
	} else {
		// Standard mode: single model query with retry
		prompt := parseQuestionAndOptions(question, options, questionType)
		var aiAnswer string
		aiAnswer, err = callOpenAIWithModel(Config.OpenAIApiBase, Config.OpenAIApiKey, prompt, Config.OpenAIModel)
		if err == nil {
			processedAnswer, _ = cleanAndValidateAnswer(aiAnswer, questionType)
		}

		// Retry once if empty/fails
		if err != nil || processedAnswer == "" {
			logWarning("首次获取答案为空或出错: %v (答案: '%s')。正在进行第二次尝试...", err, aiAnswer)
			aiAnswer, err = callOpenAIWithModel(Config.OpenAIApiBase, Config.OpenAIApiKey, prompt, Config.OpenAIModel)
			if err == nil {
				processedAnswer, _ = cleanAndValidateAnswer(aiAnswer, questionType)
			}
		}
	}

	// Final check on empty answer
	if err != nil || processedAnswer == "" {
		logError("获取答案依然为空或出错，跳过该问题。错误: %v", err)
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"code": 0,
			"msg":  "获取答案为空，已跳过",
		})
		return
	}

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

	if Config.OpenAIApiKey == "" && Config.ApiKey1 == "" && Config.ApiKey2 == "" && Config.ApiKey3 == "" {
		logCritical("未设置任何 API 密钥。请通过 -api-key 配置主密钥，或配置备选模型密钥 -api-key1 等。")
		os.Exit(1)
	}

	// Initialize HTTP Client
	initHTTPClient()

	// Initialize Cache (SQLite)
	if Config.EnableCache {
		db, err := sql.Open("sqlite", "cache.db")
		if err != nil {
			logCritical("无法打开SQLite数据库: %v", err)
			os.Exit(1)
		}
		
		db.Exec("PRAGMA journal_mode=WAL;")

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
			logCritical("创建数据库表失败: %v", err)
			os.Exit(1)
		}

		cache = NewSQLiteCache(db)
		logInfo("缓存功能已启用，已连接 SQLite 本地数据库 (cache.db)")
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

	mux.Handle("/static/", http.FileServer(http.FS(embedFS)))

	addr := fmt.Sprintf("%s:%d", Config.Host, Config.Port)
	logInfo("服务运行于: http://%s", addr)

	serverHandler := corsMiddleware(mux)

	server := &http.Server{
		Addr:    addr,
		Handler: serverHandler,
	}

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logCritical("服务启动失败: %v", err)
	}
}
