package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLoadDotEnv 验证 .env 解析规则：注释、空行、行尾注释、引号、不覆盖已有环境变量。
func TestLoadDotEnv(t *testing.T) {
	dir := t.TempDir()
	content := "# comment\nDB_HOST=db.internal\nDB_PORT=6543   # inline comment\nDB_NAME=\"my db\"\nDB_USER='quoted'\nEMPTY=\n"
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	// 预设一个环境变量，验证 .env 不会覆盖它。
	t.Setenv("DB_USER", "from-process-env")

	if err := loadDotEnv(path); err != nil {
		t.Fatalf("loadDotEnv: %v", err)
	}

	cases := map[string]string{
		"DB_HOST": "db.internal",
		"DB_PORT": "6543",
		"DB_NAME": "my db",
		"DB_USER": "from-process-env",
		"EMPTY":   "",
	}
	for key, want := range cases {
		if got := os.Getenv(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

// TestLoadDotEnvMissingFile 文件不存在不算错误（生产环境只注入环境变量）。
func TestLoadDotEnvMissingFile(t *testing.T) {
	if err := loadDotEnv(filepath.Join(t.TempDir(), "nope.env")); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

// TestEnvReaderFallbacks 确认只有"没填"才回退默认值。
func TestEnvReaderFallbacks(t *testing.T) {
	t.Setenv("X_INT", "")
	t.Setenv("X_DURATION", "   ")
	t.Setenv("X_BOOL", "")

	var reader envReader
	if got := reader.integer("X_INT", 7); got != 7 {
		t.Fatalf("integer = %d, want fallback 7", got)
	}
	if got := reader.duration("X_DURATION", time.Minute); got != time.Minute {
		t.Fatalf("duration = %v, want fallback 1m", got)
	}
	if got := reader.boolean("X_BOOL", true); !got {
		t.Fatal("boolean = false, want fallback true")
	}
	if err := reader.err(); err != nil {
		t.Fatalf("blank values must not produce errors: %v", err)
	}
}

// TestEnvReaderRejectsInvalidValues 确认类型填错会留下错误，而不是静默回退。
func TestEnvReaderRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
		read  func(*envReader)
	}{
		{
			name:  "integer",
			key:   "X_INT",
			value: "abc",
			read:  func(r *envReader) { r.integer("X_INT", 7) },
		},
		{
			name:  "duration without unit",
			key:   "X_DURATION",
			value: "10",
			read:  func(r *envReader) { r.duration("X_DURATION", time.Minute) },
		},
		{
			name:  "bool",
			key:   "X_BOOL",
			value: "maybe",
			read:  func(r *envReader) { r.boolean("X_BOOL", true) },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)

			var reader envReader
			tc.read(&reader)

			err := reader.err()
			if err == nil {
				t.Fatal("expected a parse error")
			}
			if !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("error %q does not identify %s", err, tc.key)
			}
		})
	}
}

// setRequiredEnv 填入"不配就起不来"的必填项，让用例只关心自己要验证的那部分。
// 目前必填的有两类：数据库密码、MinIO 凭据（原文存储唯一后端）。
func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DB_PASSWORD", "test-password")
	t.Setenv("MINIO_ACCESS_KEY", "mindos")
	t.Setenv("MINIO_SECRET_KEY", "test-secret")
	// 不设 MINIO_BUCKET：它的默认值 mindos 本身就非空，能满足必填校验，
	// 留给用例自己断言默认值。
}

// TestLoadRejectsInvalidTypedConfig 直接守住启动路径：多个配置同时写错时一次报全。
func TestLoadRejectsInvalidTypedConfig(t *testing.T) {
	t.Chdir(t.TempDir()) // 不读取仓库里的 .env，让测试只受显式环境变量影响
	setRequiredEnv(t)
	t.Setenv("DB_PORT", "not-a-number")
	t.Setenv("SHUTDOWN_TIMEOUT", "10")
	t.Setenv("DB_LOG_SQL", "maybe")

	_, err := Load()
	if err == nil {
		t.Fatal("expected Load to reject invalid typed config")
	}
	for _, key := range []string{"DB_PORT", "SHUTDOWN_TIMEOUT", "DB_LOG_SQL"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error %q does not include %s", err, key)
		}
	}
}

// TestLoadReadsValidTypedConfig 确认严格解析没有误伤合法值。
func TestLoadReadsValidTypedConfig(t *testing.T) {
	t.Chdir(t.TempDir())
	setRequiredEnv(t)
	t.Setenv("DB_PORT", "6543")
	t.Setenv("DB_MAX_OPEN_CONNS", "12")
	t.Setenv("DB_MAX_IDLE_CONNS", "4")
	t.Setenv("SHUTDOWN_TIMEOUT", "5s")
	t.Setenv("DB_PING_TIMEOUT", "500ms")
	t.Setenv("DB_LOG_SQL", "false")
	t.Setenv("LOG_LEVEL", "INFO")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DB.Port != 6543 {
		t.Fatalf("DB.Port = %d, want 6543", cfg.DB.Port)
	}
	if cfg.DB.MaxOpenConns != 12 || cfg.DB.MaxIdleConns != 4 {
		t.Fatalf("pool = (%d, %d), want (12, 4)", cfg.DB.MaxOpenConns, cfg.DB.MaxIdleConns)
	}
	if cfg.App.ShutdownTimeout != 5*time.Second {
		t.Fatalf("ShutdownTimeout = %v, want 5s", cfg.App.ShutdownTimeout)
	}
	if cfg.DB.PingTimeout != 500*time.Millisecond {
		t.Fatalf("PingTimeout = %v, want 500ms", cfg.DB.PingTimeout)
	}
	if cfg.DB.LogSQL {
		t.Fatal("DB.LogSQL = true, want false")
	}
	if cfg.Log.Level != "info" {
		t.Fatalf("Log.Level = %q, want normalized info", cfg.Log.Level)
	}
}

// TestLoadUsesDefaultsForBlankValues 确认空字符串仍按"未配置"处理，不误伤本地默认配置。
func TestLoadUsesDefaultsForBlankValues(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("APP_ENV", "")
	t.Setenv("DB_PORT", "")
	t.Setenv("DB_MAX_OPEN_CONNS", "")
	t.Setenv("SHUTDOWN_TIMEOUT", "")
	t.Setenv("DB_LOG_SQL", "")
	setRequiredEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.App.ShutdownTimeout != 10*time.Second {
		t.Fatalf("ShutdownTimeout = %v, want 10s", cfg.App.ShutdownTimeout)
	}
	if cfg.DB.Port != 5432 {
		t.Fatalf("DB.Port = %d, want 5432", cfg.DB.Port)
	}
	if cfg.DB.MaxOpenConns != 25 {
		t.Fatalf("MaxOpenConns = %d, want 25", cfg.DB.MaxOpenConns)
	}
	if !cfg.DB.LogSQL {
		t.Fatal("DB.LogSQL = false, want dev default true")
	}
}

// TestLoadNewPhase2Defaults 确认 Phase 2 新增配置的默认值与"全空即未配置"语义。
func TestLoadNewPhase2Defaults(t *testing.T) {
	t.Chdir(t.TempDir())
	setRequiredEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Storage.MinIO.Endpoint != "localhost:9000" || cfg.Storage.MinIO.Bucket != "mindos" {
		t.Fatalf("Storage.MinIO = %+v, want localhost:9000/mindos", cfg.Storage.MinIO)
	}
	if cfg.IsLLMConfigured() {
		t.Fatal("LLM should be unconfigured by default")
	}
	if cfg.Embedding.APIBase != "" || cfg.Embedding.APIKey != "" || cfg.Embedding.Model != "" {
		t.Fatalf("Embedding should be empty by default, got %+v", cfg.Embedding)
	}
	if cfg.Agent.MaxSteps != 30 || cfg.Agent.TokenBudget != 200000 || cfg.Agent.RunTimeout != 5*time.Minute {
		t.Fatalf("Agent defaults wrong: %+v", cfg.Agent)
	}
}

// TestLoadReadsModelAndBudgetConfig 确认模型与预算配置按填写值生效。
func TestLoadReadsModelAndBudgetConfig(t *testing.T) {
	t.Chdir(t.TempDir())
	setRequiredEnv(t)
	// MinIO 是唯一存储后端，凭据属于必填项，这里给够最小可用配置。
	t.Setenv("MINIO_ACCESS_KEY", "mindos")
	t.Setenv("MINIO_SECRET_KEY", "test-secret")
	t.Setenv("MINIO_BUCKET", "mindos-test")
	t.Setenv("LLM_API_BASE", "https://api.example.com/v1/")
	t.Setenv("LLM_API_KEY", "sk-1234567890abcdef")
	t.Setenv("LLM_MODEL", "gpt-4o-mini")
	t.Setenv("AGENT_MAX_STEPS", "15")
	t.Setenv("AGENT_TOKEN_BUDGET", "50000")
	t.Setenv("AGENT_RUN_TIMEOUT", "2m")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// 结尾斜杠被归一化掉，避免拼 URL 时出现双斜杠。
	if cfg.LLM.APIBase != "https://api.example.com/v1" {
		t.Fatalf("LLM.APIBase = %q, want trailing slash trimmed", cfg.LLM.APIBase)
	}
	if !cfg.IsLLMConfigured() {
		t.Fatal("LLM should be configured when all three fields are set")
	}
	if cfg.Storage.MinIO.Bucket != "mindos-test" {
		t.Fatalf("Storage.MinIO.Bucket = %q", cfg.Storage.MinIO.Bucket)
	}
	if cfg.Agent.MaxSteps != 15 || cfg.Agent.TokenBudget != 50000 || cfg.Agent.RunTimeout != 2*time.Minute {
		t.Fatalf("Agent = %+v", cfg.Agent)
	}
}

// TestLoadRejectsPartialModelConfig 确认模型配置只配一半时启动报错并指出缺哪几项。
func TestLoadRejectsPartialModelConfig(t *testing.T) {
	t.Chdir(t.TempDir())
	setRequiredEnv(t)
	t.Setenv("LLM_API_KEY", "sk-only-key")

	_, err := Load()
	if err == nil {
		t.Fatal("expected Load to reject partial LLM config")
	}
	for _, key := range []string{"LLM_API_BASE", "LLM_MODEL"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error %q does not include missing key %s", err, key)
		}
	}
}

// TestValidate 覆盖必然阻止服务启动的语义错误。
func TestValidate(t *testing.T) {
	valid := func() *Config {
		return &Config{
			App: AppConfig{Env: "dev", ShutdownTimeout: 10 * time.Second},
			Log: LogConfig{Level: "debug", Format: "console"},
			DB: DBConfig{
				Host:         "localhost",
				Port:         5432,
				Password:     "pw",
				MaxOpenConns: 25,
				MaxIdleConns: 10,
				PingTimeout:  3 * time.Second,
			},
			// MinIO 是唯一存储后端，这一组用例关心的是各字段自身的校验，
			// 存储项的必填校验由下面三条 minio 用例单独覆盖。
			Storage: StorageConfig{
				MaxUploadBytes: 10 << 20,
				MinIO: MinIOConfig{
					Endpoint:  "localhost:9000",
					AccessKey: "key",
					SecretKey: "secret",
					Bucket:    "mindos",
				},
			},
		}
	}

	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{name: "app env", mutate: func(c *Config) { c.App.Env = "staging" }, want: "APP_ENV"},
		{name: "log level", mutate: func(c *Config) { c.Log.Level = "verbose" }, want: "LOG_LEVEL"},
		{name: "log format", mutate: func(c *Config) { c.Log.Format = "text" }, want: "LOG_FORMAT"},
		{name: "shutdown timeout", mutate: func(c *Config) { c.App.ShutdownTimeout = 0 }, want: "SHUTDOWN_TIMEOUT"},
		{name: "db host", mutate: func(c *Config) { c.DB.Host = "" }, want: "DB_HOST"},
		{name: "db port", mutate: func(c *Config) { c.DB.Port = 70000 }, want: "DB_PORT"},
		{name: "db password", mutate: func(c *Config) { c.DB.Password = "" }, want: "DB_PASSWORD"},
		{name: "max open conns", mutate: func(c *Config) { c.DB.MaxOpenConns = 0 }, want: "DB_MAX_OPEN_CONNS"},
		{name: "max idle conns", mutate: func(c *Config) { c.DB.MaxIdleConns = -1 }, want: "DB_MAX_IDLE_CONNS"},
		{name: "ping timeout", mutate: func(c *Config) { c.DB.PingTimeout = 0 }, want: "DB_PING_TIMEOUT"},
		{name: "minio endpoint empty", mutate: func(c *Config) { c.Storage.MinIO.Endpoint = " " }, want: "MINIO_ENDPOINT"},
		{name: "minio endpoint with scheme", mutate: func(c *Config) {
			c.Storage.MinIO.Endpoint = "http://localhost:9000"
		}, want: "MINIO_ENDPOINT"},
		{name: "minio credentials missing", mutate: func(c *Config) {
			c.Storage.MinIO.AccessKey = ""
		}, want: "MINIO_ACCESS_KEY"},
		{name: "minio bucket missing", mutate: func(c *Config) {
			c.Storage.MinIO.Bucket = " "
		}, want: "MINIO_BUCKET"},
		{name: "agent max steps", mutate: func(c *Config) { c.Agent.MaxSteps = 0 }, want: "AGENT_MAX_STEPS"},
		{name: "agent token budget", mutate: func(c *Config) { c.Agent.TokenBudget = 0 }, want: "AGENT_TOKEN_BUDGET"},
		{name: "agent run timeout", mutate: func(c *Config) { c.Agent.RunTimeout = 0 }, want: "AGENT_RUN_TIMEOUT"},
		{name: "llm partial", mutate: func(c *Config) {
			c.LLM.APIKey = "sk-test"
		}, want: "LLM_API_BASE"},
		{name: "embedding partial", mutate: func(c *Config) {
			c.Embedding.APIBase = "https://x"
			c.Embedding.APIKey = "k"
		}, want: "EMBEDDING_MODEL"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := valid()
			tc.mutate(cfg)
			err := cfg.validate()
			if err == nil {
				t.Fatal("expected validation error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not include %s", err, tc.want)
			}
		})
	}
}
