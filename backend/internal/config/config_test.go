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

// TestLoadRejectsInvalidTypedConfig 直接守住启动路径：多个配置同时写错时一次报全。
func TestLoadRejectsInvalidTypedConfig(t *testing.T) {
	t.Chdir(t.TempDir()) // 不读取仓库里的 .env，让测试只受显式环境变量影响
	t.Setenv("DB_PASSWORD", "test-password")
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

// TestLoadUsesDefaultsForBlankValues 确认空字符串仍按"未配置"处理，不误伤本地默认配置。
func TestLoadUsesDefaultsForBlankValues(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("APP_ENV", "")
	t.Setenv("DB_PORT", "")
	t.Setenv("DB_MAX_OPEN_CONNS", "")
	t.Setenv("SHUTDOWN_TIMEOUT", "")
	t.Setenv("DB_LOG_SQL", "")
	t.Setenv("DB_PASSWORD", "test-password")

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
