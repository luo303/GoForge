package handler

import (
	"fmt"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/luo303/mindos/backend/internal/config"
	"github.com/luo303/mindos/backend/internal/httpx"
)

// SettingsHandler 提供只读的配置视图（GET /settings）。
//
// 安全边界：这个接口面向"本地单用户"也不返回完整密钥——
// 浏览器、终端历史、日志抓取都有可能把响应内容带出去。
// 只输出"是否已配置 + 脱敏片段"，足够人确认"配的是不是那把 Key"。
type SettingsHandler struct {
	cfg *config.Config
}

// NewSettingsHandler 创建设置视图 handler。持有的是只读配置指针，
// handler 不提供任何写接口，修改配置只能改 .env 后重启。
func NewSettingsHandler(cfg *config.Config) *SettingsHandler {
	return &SettingsHandler{cfg: cfg}
}

// modelSettingsView 是单个模型配置的对外视图。
type modelSettingsView struct {
	APIBase          string `json:"api_base"`
	Model            string `json:"model"`
	APIKeyMasked     string `json:"api_key_masked"` // 空串表示未配置
	APIKeyConfigured bool   `json:"api_key_configured"`
}

// settingsView 是 /settings 的响应体。字段与 config.Config 一一对应，
// 但只包含"可以给用户看"的子集——密码字段一律不出现，密钥只给脱敏值。
type settingsView struct {
	App struct {
		Env      string `json:"env"`
		HTTPAddr string `json:"http_addr"`
	} `json:"app"`
	Log struct {
		Level  string `json:"level"`
		Format string `json:"format"`
	} `json:"log"`
	DB struct {
		Host    string `json:"host"`
		Port    int    `json:"port"`
		Name    string `json:"name"`
		SSLMode string `json:"sslmode"`
		User    string `json:"user"`
	} `json:"db"`
	Redis struct {
		Addr               string `json:"addr"`
		DB                 int    `json:"db"`
		PoolSize           int    `json:"pool_size"`
		PasswordConfigured bool   `json:"password_configured"`
	} `json:"redis"`
	Storage struct {
		Dir            string `json:"dir"`
		MaxUploadBytes int64  `json:"max_upload_bytes"`
	} `json:"storage"`
	LLM       modelSettingsView `json:"llm"`
	Embedding modelSettingsView `json:"embedding"`
	Agent     struct {
		MaxSteps         int   `json:"max_steps"`
		TokenBudget      int64 `json:"token_budget"`
		RunTimeoutSecond int64 `json:"run_timeout_seconds"`
	} `json:"agent"`
}

// Get 返回当前配置视图。
func (h *SettingsHandler) Get(c *gin.Context) {
	cfg := h.cfg
	view := settingsView{}

	view.App.Env = cfg.App.Env
	view.App.HTTPAddr = cfg.App.HTTPAddr
	view.Log.Level = cfg.Log.Level
	view.Log.Format = cfg.Log.Format
	view.DB.Host = cfg.DB.Host
	view.DB.Port = cfg.DB.Port
	view.DB.Name = cfg.DB.Name
	view.DB.SSLMode = cfg.DB.SSLMode
	view.DB.User = cfg.DB.User
	view.Redis.Addr = cfg.Redis.Addr
	view.Redis.DB = cfg.Redis.DB
	view.Redis.PoolSize = cfg.Redis.PoolSize
	view.Redis.PasswordConfigured = cfg.Redis.Password != ""
	view.Storage.Dir = cfg.Storage.Dir
	view.Storage.MaxUploadBytes = cfg.Storage.MaxUploadBytes
	view.LLM = modelView(cfg.LLM.APIBase, cfg.LLM.APIKey, cfg.LLM.Model)
	view.Embedding = modelView(cfg.Embedding.APIBase, cfg.Embedding.APIKey, cfg.Embedding.Model)
	view.Agent.MaxSteps = cfg.Agent.MaxSteps
	view.Agent.TokenBudget = cfg.Agent.TokenBudget
	view.Agent.RunTimeoutSecond = int64(cfg.Agent.RunTimeout / time.Second)

	httpx.OK(c, view)
}

func modelView(apiBase, apiKey, model string) modelSettingsView {
	return modelSettingsView{
		APIBase:          apiBase,
		Model:            model,
		APIKeyMasked:     maskSecret(apiKey),
		APIKeyConfigured: apiKey != "",
	}
}

// maskSecret 把密钥脱敏成"前 4 位 **** 后 4 位"。
//
// 长度不足 9 的密钥信息量本来就低，直接全遮；
// 返回值永远不包含原始完整内容，调用方无法"忘了遮"。
func maskSecret(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 8 {
		return "****"
	}
	return fmt.Sprintf("%s****%s", s[:4], s[len(s)-4:])
}
