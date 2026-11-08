// Package config 负责把外部配置（.env 文件 / 环境变量）加载成强类型的 Config，
// 并在启动阶段一次性完成校验。
//
// 设计原则：配置错误属于"程序根本没法正常工作"的错误，应该在启动时立刻失败，
// 而不是等到第一个请求进来才 panic。这和 NestJS ConfigModule 的 validationSchema 是一个思路。
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config 是应用的全部配置，字段分组与 .env 中的区块一一对应。
type Config struct {
	App   AppConfig
	Log   LogConfig
	DB    DBConfig
	Redis RedisConfig
	JWT   JWTConfig
}

// AppConfig 控制 HTTP 服务本身。
type AppConfig struct {
	Env             string        // dev | prod
	HTTPAddr        string        // 例如 ":8080"
	ShutdownTimeout time.Duration // 优雅关闭的最长等待时间
}

// LogConfig 控制日志输出。
type LogConfig struct {
	Level  string // debug | info | warn | error
	Format string // console | json
}

// DBConfig 描述 PostgreSQL 连接。
type DBConfig struct {
	Host            string
	Port            int
	User            string
	Password        string
	Name            string
	SSLMode         string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	PingTimeout     time.Duration
	// LogSQL 为 true 时打印每一条 SQL（开发环境默认开启）。
	LogSQL bool
}

// DSN 返回 gorm.io/driver/postgres 需要的连接串（key=value 形式）。
// 敏感字段只在内存中拼装，不要打日志。
func (c DBConfig) DSN() string {
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s TimeZone=UTC",
		c.Host, c.Port, c.User, c.Password, c.Name, c.SSLMode,
	)
}

// RedisConfig 描述 Redis 连接（阶段四开始使用）。
type RedisConfig struct {
	Addr     string
	Password string
	DB       int
	PoolSize int
}

// JWTConfig 描述令牌签发参数（阶段三开始使用）。
type JWTConfig struct {
	Secret    string
	AccessTTL time.Duration
}

// IsDev 用于在开发环境输出更详细的错误信息。
func (c *Config) IsDev() bool { return c.App.Env == "dev" }

// Load 读取 .env（存在才读）与环境变量并校验。
// 优先级：进程环境变量 > .env 文件 > 默认值。
func Load() (*Config, error) {
	if err := loadDotEnv(".env"); err != nil {
		return nil, fmt.Errorf("load .env: %w", err)
	}

	env := getEnv("APP_ENV", "dev")
	cfg := &Config{
		App: AppConfig{
			Env:             env,
			HTTPAddr:        getEnv("HTTP_ADDR", ":8080"),
			ShutdownTimeout: getEnvDuration("SHUTDOWN_TIMEOUT", 10*time.Second),
		},
		Log: LogConfig{
			Level:  getEnv("LOG_LEVEL", "debug"),
			Format: getEnv("LOG_FORMAT", "console"),
		},
		DB: DBConfig{
			Host:            getEnv("DB_HOST", "localhost"),
			Port:            getEnvInt("DB_PORT", 5432),
			User:            getEnv("DB_USER", "goforge"),
			Password:        getEnv("DB_PASSWORD", ""),
			Name:            getEnv("DB_NAME", "goforge"),
			SSLMode:         getEnv("DB_SSLMODE", "disable"),
			MaxOpenConns:    getEnvInt("DB_MAX_OPEN_CONNS", 25),
			MaxIdleConns:    getEnvInt("DB_MAX_IDLE_CONNS", 10),
			ConnMaxLifetime: getEnvDuration("DB_CONN_MAX_LIFETIME", 30*time.Minute),
			PingTimeout:     getEnvDuration("DB_PING_TIMEOUT", 3*time.Second),
			LogSQL:          getEnvBool("DB_LOG_SQL", env == "dev"),
		},
		Redis: RedisConfig{
			Addr:     getEnv("REDIS_ADDR", "localhost:6379"),
			Password: getEnv("REDIS_PASSWORD", ""),
			DB:       getEnvInt("REDIS_DB", 0),
			PoolSize: getEnvInt("REDIS_POOL_SIZE", 20),
		},
		JWT: JWTConfig{
			Secret:    getEnv("JWT_SECRET", ""),
			AccessTTL: getEnvDuration("JWT_ACCESS_TTL", 24*time.Hour),
		},
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// validate 只校验"必然出错"的项，而不是把每个字段都机械地检查一遍。
func (c *Config) validate() error {
	var errs []error
	switch c.App.Env {
	case "dev", "prod":
	default:
		errs = append(errs, fmt.Errorf("APP_ENV must be dev or prod, got %q", c.App.Env))
	}
	switch c.Log.Format {
	case "console", "json":
	default:
		errs = append(errs, fmt.Errorf("LOG_FORMAT must be console or json, got %q", c.Log.Format))
	}
	if c.DB.Host == "" {
		errs = append(errs, errors.New("DB_HOST is required"))
	}
	if c.DB.Password == "" {
		errs = append(errs, errors.New("DB_PASSWORD is required"))
	}
	// errors.Join 是 Go 1.20 加入的：把多个错误合成一个，报错时一次看全。
	return errors.Join(errs...)
}

// loadDotEnv 解析一个极简的 KEY=VALUE 文件。
//
// 为什么不用 godotenv / viper？这里需要支持的语法只有注释、空行、KEY=VALUE、
// 引号和行尾注释，几十行标准库代码比多一个依赖更可控，也更容易解释。
// 它不会覆盖已经存在的环境变量，所以容器平台注入的变量优先级更高。
func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil // 文件不存在是合法的：生产环境只注入环境变量
	}
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("%s:%d: invalid line %q", path, lineNo, line)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if unquoted, ok := cutQuoted(value); ok {
			value = unquoted
		} else if idx := strings.Index(value, " #"); idx >= 0 {
			// 去掉行尾注释：DB_PORT=5432  # 端口
			value = strings.TrimSpace(value[:idx])
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// cutQuoted 去掉首尾成对的单/双引号，保留值内部的空格。
func cutQuoted(v string) (string, bool) {
	if len(v) < 2 {
		return "", false
	}
	if v[0] == '"' && v[len(v)-1] == '"' {
		return v[1 : len(v)-1], true
	}
	if v[0] == '\'' && v[len(v)-1] == '\'' {
		return v[1 : len(v)-1], true
	}
	return "", false
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

// getEnvBool 只接受标准写法：1/0、true/false、yes/no、on/off（不区分大小写）。
// 其它值一律回退到默认值，而不是静默当成 true —— 配置写错时行为要可预测。
func getEnvBool(key string, fallback bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}
