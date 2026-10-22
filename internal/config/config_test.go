package config

import (
	"os"
	"path/filepath"
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

// TestGetEnvDuration 覆盖合法的与非法的时间字符串。
func TestGetEnvDuration(t *testing.T) {
	t.Setenv("X_TTL", "90s")
	if got := getEnvDuration("X_TTL", time.Minute); got != 90*time.Second {
		t.Fatalf("got %v, want 90s", got)
	}
	t.Setenv("X_TTL", "not-a-duration")
	if got := getEnvDuration("X_TTL", time.Minute); got != time.Minute {
		t.Fatalf("got %v, want fallback 1m", got)
	}
}

// TestValidate 确认非法环境值会被拒绝。
func TestValidate(t *testing.T) {
	cfg := &Config{
		App: AppConfig{Env: "staging"},
		Log: LogConfig{Format: "console"},
		DB:  DBConfig{Host: "localhost", Password: "pw"},
	}
	if err := cfg.validate(); err == nil {
		t.Fatal("expected validation error for APP_ENV=staging")
	}
}
