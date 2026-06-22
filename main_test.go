package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFallbackMode(t *testing.T) {
	// Initialize default config
	Config.OpenAIApiKey = "mock-key"
	Config.MultiModelMode = "fallback"
	Config.Model1 = "model-fail"
	Config.Model2 = "model-success"
	Config.Model3 = ""
	Config.EnableCache = false

	// Mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		bodyStr := string(bodyBytes)

		if strings.Contains(bodyStr, `"model":"model-fail"`) {
			// Fail this model
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error": "internal error"}`))
			return
		}

		if strings.Contains(bodyStr, `"model":"model-success"`) {
			// Return valid choice
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"choices": [
					{
						"message": {
							"role": "assistant",
							"content": "B"
						}
					}
				]
			}`))
			return
		}

		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	Config.OpenAIApiBase = server.URL
	initHTTPClient()

	// Perform a mock handleSearch request
	req := httptest.NewRequest("POST", "/api/search", strings.NewReader(`{"title": "Question 1", "type": "single", "options": "A. Yes\nB. No"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handleSearch(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected HTTP 200, got %d", resp.StatusCode)
	}

	var res map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&res)

	if res["code"].(float64) != 1 {
		t.Errorf("Expected code 1, got %v", res["code"])
	}
	if res["answer"].(string) != "B" {
		t.Errorf("Expected answer 'B' (from model-success), got '%s'", res["answer"])
	}
}

func TestConfidenceModeWithSearch(t *testing.T) {
	Config.OpenAIApiKey = "mock-key"
	Config.OpenAIModel = "mock-gpt"
	Config.MultiModelMode = "confidence"
	Config.ConfidenceThreshold = 0.8
	Config.ExaApiKey = "mock-exa"
	Config.EnableCache = false

	var calledSearch = false
	var calledSecondQuery = false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Detect request path
		if r.URL.Path == "/search" {
			calledSearch = true
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"results": [
					{
						"title": "Mock Search Title",
						"url": "http://mock.com",
						"highlights": ["Found correct answer is B"]
					}
				]
			}`))
			return
		}

		bodyBytes, _ := io.ReadAll(r.Body)
		bodyStr := string(bodyBytes)

		// 1. Initial answer call
		if strings.Contains(bodyStr, "Question 1") && !strings.Contains(bodyStr, "第二次回答") && !strings.Contains(bodyStr, "评估这个答案") {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"choices": [
					{
						"message": {
							"role": "assistant",
							"content": "A"
						}
					}
				]
			}`))
			return
		}

		// 2. Confidence scoring call
		if strings.Contains(bodyStr, "评估这个答案") {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"choices": [
					{
						"message": {
							"role": "assistant",
							"content": "0.5"
						}
					}
				]
			}`))
			return
		}

		// 3. Second query with search context
		if strings.Contains(bodyStr, "第二次回答") && strings.Contains(bodyStr, "Found correct answer is B") {
			calledSecondQuery = true
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"choices": [
					{
						"message": {
							"role": "assistant",
							"content": "B"
						}
					}
				]
			}`))
			return
		}

		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	Config.OpenAIApiBase = server.URL
	Config.ExaBaseUrl = server.URL
	initHTTPClient()

	req := httptest.NewRequest("POST", "/api/search", strings.NewReader(`{"title": "Question 1", "type": "single", "options": "A. Yes\nB. No"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handleSearch(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected HTTP 200, got %d", resp.StatusCode)
	}

	var res map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&res)

	if !calledSearch {
		t.Error("Expected Exa search to be called due to low confidence (0.5 < 0.8)")
	}
	if !calledSecondQuery {
		t.Error("Expected second query with search context to be called")
	}
	if res["answer"].(string) != "B" {
		t.Errorf("Expected final answer to be 'B', got '%s'", res["answer"])
	}
}

func TestCleanAndValidateAnswer(t *testing.T) {
	tests := []struct {
		aiResp string
		qType  string
		want   string
		valid  bool
	}{
		{"A", "single", "A", true},
		{"a", "single", "A", true},
		{"Option A is correct", "single", "A", true},
		{"A#B", "multiple", "A#B", true},
		{"B#A", "multiple", "A#B", true}, // should sort
		{"对", "judgement", "正确", true},
		{"错误", "judgement", "错误", true},
		{"", "single", "", false},
	}

	for _, tt := range tests {
		got, valid := cleanAndValidateAnswer(tt.aiResp, tt.qType)
		if got != tt.want || valid != tt.valid {
			t.Errorf("cleanAndValidateAnswer(%q, %q) = (%q, %t); want (%q, %t)", tt.aiResp, tt.qType, got, valid, tt.want, tt.valid)
		}
	}
}

func TestFallbackModeMultipleEndpoints(t *testing.T) {
	// Initialize default config
	Config.OpenAIApiKey = "main-key"
	Config.OpenAIApiBase = "http://main-base"
	Config.MultiModelMode = "fallback"
	Config.EnableCache = false

	var server1Called, server2Called bool

	// Server 1
	server1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server1Called = true
		// Verify auth header on server 1
		auth := r.Header.Get("Authorization")
		if auth != "Bearer key-1" {
			t.Errorf("Expected bearer key-1 on server 1, got '%s'", auth)
		}
		// Return 500
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error": "fail"}`))
	}))
	defer server1.Close()

	// Server 2
	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server2Called = true
		// Verify auth header on server 2
		auth := r.Header.Get("Authorization")
		if auth != "Bearer key-2" {
			t.Errorf("Expected bearer key-2 on server 2, got '%s'", auth)
		}
		// Return 200 with valid choice
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"choices": [
				{
					"message": {
						"role": "assistant",
						"content": "C"
					}
				}
			]
		}`))
	}))
	defer server2.Close()

	Config.ApiBase1 = server1.URL
	Config.ApiKey1 = "key-1"
	Config.Model1 = "model-on-server-1"

	Config.ApiBase2 = server2.URL
	Config.ApiKey2 = "key-2"
	Config.Model2 = "model-on-server-2"

	Config.ApiBase3 = ""
	Config.ApiKey3 = ""
	Config.Model3 = ""

	initHTTPClient()

	req := httptest.NewRequest("POST", "/api/search", strings.NewReader(`{"title": "Question Multiple Endpoints", "type": "single", "options": "A. X\nB. Y\nC. Z"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handleSearch(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected HTTP 200, got %d", resp.StatusCode)
	}

	var res map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&res)

	if !server1Called {
		t.Error("Expected server 1 to be called")
	}
	if !server2Called {
		t.Error("Expected server 2 to be called")
	}
	if res["answer"].(string) != "C" {
		t.Errorf("Expected answer 'C', got '%s'", res["answer"])
	}
}

func TestFallbackModeWithConfidenceCascade(t *testing.T) {
	Config.OpenAIApiKey = "mock-key"
	Config.MultiModelMode = "fallback"
	Config.ConfidenceThreshold = 0.8
	Config.ExaApiKey = "mock-exa"
	Config.EnableCache = false

	var calledSearch = false
	var calledEval = false
	var calledModel2SearchAnswer = false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search" {
			calledSearch = true
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"results": [
					{
						"title": "Search Result",
						"url": "http://mock.com",
						"highlights": ["Found correct answer is B"]
					}
				]
			}`))
			return
		}

		bodyBytes, _ := io.ReadAll(r.Body)
		bodyStr := string(bodyBytes)

		// Model 1 initial answer
		if strings.Contains(bodyStr, `"model":"model-1-answering"`) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"choices": [
					{
						"message": {
							"role": "assistant",
							"content": "A"
						}
					}
				]
			}`))
			return
		}

		// Model 2 confidence evaluation of Model 1's answer
		if strings.Contains(bodyStr, `"model":"model-2-evaluating-and-searching"`) && strings.Contains(bodyStr, "评估这个答案") {
			calledEval = true
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"choices": [
					{
						"message": {
							"role": "assistant",
							"content": "0.5"
						}
					}
				]
			}`))
			return
		}

		// Model 2 search-enhanced answer
		if strings.Contains(bodyStr, `"model":"model-2-evaluating-and-searching"`) && strings.Contains(bodyStr, "Found correct answer is B") {
			calledModel2SearchAnswer = true
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"choices": [
					{
						"message": {
							"role": "assistant",
							"content": "B"
						}
					}
				]
			}`))
			return
		}

		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	Config.ApiBase1 = server.URL
	Config.ApiKey1 = "key-1"
	Config.Model1 = "model-1-answering"

	Config.ApiBase2 = server.URL
	Config.ApiKey2 = "key-2"
	Config.Model2 = "model-2-evaluating-and-searching"

	Config.ApiBase3 = ""
	Config.ApiKey3 = ""
	Config.Model3 = ""

	Config.OpenAIApiBase = server.URL
	Config.ExaBaseUrl = server.URL
	initHTTPClient()

	req := httptest.NewRequest("POST", "/api/search", strings.NewReader(`{"title": "Question Cascade Test", "type": "single", "options": "A. Yes\nB. No"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handleSearch(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected HTTP 200, got %d", resp.StatusCode)
	}

	var res map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&res)

	if !calledEval {
		t.Error("Expected Model 2 to evaluate Model 1's answer")
	}
	if !calledSearch {
		t.Error("Expected Exa search to be called after low confidence evaluation")
	}
	if !calledModel2SearchAnswer {
		t.Error("Expected Model 2 to answer using search context")
	}
	if res["answer"].(string) != "B" {
		t.Errorf("Expected final answer to be 'B', got '%s'", res["answer"])
	}
}
