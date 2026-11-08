package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/luo303/mindos/backend/internal/config"
)

func TestMaskSecret(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},                                // 未配置
		{"short", "****"},                       // 短密钥全遮
		{"sk-1234567890abcdef", "sk-1****cdef"}, // 常规：前 4 后 4
		{"12345678", "****"},                    // 恰好 8 位也全遮（信息量低）
		{"123456789", "1234****6789"},           // 9 位起才露片段
	}
	for _, tc := range cases {
		if got := maskSecret(tc.in); got != tc.want {
			t.Errorf("maskSecret(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestSettingsGetMasksSecrets 是安全红线测试：响应里任何地方都不能出现完整密钥。
func TestSettingsGetMasksSecrets(t *testing.T) {
	const apiKey = "sk-live-0123456789abcdef"
	cfg := &config.Config{
		App:   config.AppConfig{Env: "dev", HTTPAddr: ":8080"},
		Log:   config.LogConfig{Level: "debug", Format: "console"},
		DB:    config.DBConfig{Host: "localhost", Port: 5432, Name: "mindos", User: "mindos", Password: "db-password"},
		LLM:   config.LLMConfig{APIBase: "https://api.example.com/v1", APIKey: apiKey, Model: "gpt-4o-mini"},
		Agent: config.AgentConfig{MaxSteps: 30, TokenBudget: 200000, RunTimeout: 5 * time.Minute},
	}
	h := NewSettingsHandler(cfg)

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	h.Get(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200", rec.Code)
	}
	body := rec.Body.String()

	if strings.Contains(body, apiKey) {
		t.Fatalf("response leaks the full API key: %s", body)
	}
	if strings.Contains(body, "db-password") {
		t.Fatalf("response leaks the DB password: %s", body)
	}

	var parsed struct {
		Data struct {
			LLM struct {
				APIKeyMasked     string `json:"api_key_masked"`
				APIKeyConfigured bool   `json:"api_key_configured"`
				Model            string `json:"model"`
			} `json:"llm"`
			Agent struct {
				RunTimeoutSecond int64 `json:"run_timeout_seconds"`
			} `json:"agent"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !parsed.Data.LLM.APIKeyConfigured {
		t.Error("llm.api_key_configured should be true")
	}
	if parsed.Data.LLM.APIKeyMasked == "" {
		t.Error("llm.api_key_masked should not be empty")
	}
	if parsed.Data.LLM.Model != "gpt-4o-mini" {
		t.Errorf("llm.model = %q", parsed.Data.LLM.Model)
	}
	if parsed.Data.Agent.RunTimeoutSecond != 300 {
		t.Errorf("agent.run_timeout_seconds = %d, want 300", parsed.Data.Agent.RunTimeoutSecond)
	}
}

// TestSettingsGetUnconfiguredModel 确认模型未配置时如实返回，不伪造成功状态。
func TestSettingsGetUnconfiguredModel(t *testing.T) {
	cfg := &config.Config{
		App: config.AppConfig{Env: "dev", HTTPAddr: ":8080"},
		Log: config.LogConfig{Level: "debug", Format: "console"},
		DB:  config.DBConfig{Host: "localhost", Port: 5432, Name: "mindos", User: "mindos", Password: "pw"},
	}
	h := NewSettingsHandler(cfg)

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	h.Get(c)

	var parsed struct {
		Data struct {
			LLM struct {
				APIKeyMasked     string `json:"api_key_masked"`
				APIKeyConfigured bool   `json:"api_key_configured"`
			} `json:"llm"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.Data.LLM.APIKeyConfigured {
		t.Error("llm.api_key_configured should be false when unset")
	}
	if parsed.Data.LLM.APIKeyMasked != "" {
		t.Errorf("llm.api_key_masked should be empty when unset, got %q", parsed.Data.LLM.APIKeyMasked)
	}
}
