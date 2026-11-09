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
	"sort"
	"strconv"
	"strings"
	"time"
)

// Config 是应用的全部配置，字段分组与 .env 中的区块一一对应。
//
// MindOS 是单用户本地应用：没有账号体系，因此这里不存在 JWT / 认证相关配置。
// 需要新增配置时，必须同时满足两点——代码真的读了它，且 .env.example 里有对应项。
type Config struct {
	App       AppConfig
	Log       LogConfig
	DB        DBConfig
	Redis     RedisConfig
	Storage   StorageConfig
	LLM       LLMConfig
	Embedding EmbeddingConfig
	Agent     AgentConfig
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

// RedisConfig 描述 Redis 连接。单用户本地场景暂不依赖 Redis，
// 只有缓存/限流成为真实瓶颈时才接线（见 docs/plan.md 的顺序约束）。
type RedisConfig struct {
	Addr     string
	Password string
	DB       int
	PoolSize int
}

// IsLLMConfigured 报告 LLM 是否已配置到"可用"程度。
// Phase 9 才会真正调用模型；这里只回答"配了没有"，供 /settings 与 /system/status 展示，
// 未配置是合法状态（用户可能还没填 Key），不算启动错误。
func (c *Config) IsLLMConfigured() bool {
	return c.LLM.APIBase != "" && c.LLM.APIKey != "" && c.LLM.Model != ""
}

// StorageConfig 描述本地文件存储（上传的原始文档保存在这里）。
// 单用户本地部署用文件系统即可；对象存储对单机是多余的运维负担（见 overview 取舍表）。
type StorageConfig struct {
	Dir string
	// MaxUploadBytes 是单次导入允许的最大字节数。
	// 放在配置里而不是代码常量：它取决于机器内存与使用习惯，不是业务规则；
	// 但必须在服务端强制，不能只靠前端 accept 与 input 限制。
	MaxUploadBytes int64
}

// LLMConfig 描述对话模型接入参数（OpenAI 兼容接口）。
// 三个字段要么全配、要么全空；本阶段只做读取与校验，调用在 Phase 9 接入。
type LLMConfig struct {
	APIBase string // 例如 https://api.openai.com/v1
	APIKey  string // 只从环境变量读，禁止入库、禁止进日志
	Model   string // 例如 gpt-4o-mini
}

// EmbeddingConfig 描述向量模型接入参数。维度在 overview 中固定为 1536，
// 换模型要走"新增列 → 回填 → 切换"的迁移流程，不是改配置。
type EmbeddingConfig struct {
	APIBase string
	APIKey  string
	Model   string
}

// AgentConfig 是 Agent 运行的三重预算（AGENTS.md 硬性约束 #10）。
// 预算配在启动配置里而不是数据库：预算是"护栏"不是"业务数据"，
// 改它需要重启服务，正好强制人停下来想清楚。
type AgentConfig struct {
	MaxSteps    int           // 单次运行最大工具调用步数
	TokenBudget int64         // 单次运行最大 token 消耗（记账用整数）
	RunTimeout  time.Duration // 单次运行整体超时
}

// IsDev 用于在开发环境输出更详细的错误信息。
func (c *Config) IsDev() bool { return c.App.Env == "dev" }

// Load 读取 .env（存在才读）与环境变量并校验。
// 优先级：进程环境变量 > .env 文件 > 默认值。
func Load() (*Config, error) {
	if err := loadDotEnv(".env"); err != nil {
		return nil, fmt.Errorf("load .env: %w", err)
	}

	// reader 在解析阶段收集所有类型错误，最后一次性返回。
	// 这样 DB_PORT 和 SHUTDOWN_TIMEOUT 同时写错时，不用改一个、重启、再发现下一个。
	reader := &envReader{}
	env := reader.text("APP_ENV", "dev")
	cfg := &Config{
		App: AppConfig{
			Env:             env,
			HTTPAddr:        reader.text("HTTP_ADDR", ":8080"),
			ShutdownTimeout: reader.duration("SHUTDOWN_TIMEOUT", 10*time.Second),
		},
		Log: LogConfig{
			Level:  strings.ToLower(reader.text("LOG_LEVEL", "debug")),
			Format: reader.text("LOG_FORMAT", "console"),
		},
		DB: DBConfig{
			Host:            reader.text("DB_HOST", "localhost"),
			Port:            reader.integer("DB_PORT", 5432),
			User:            reader.text("DB_USER", "mindos"),
			Password:        reader.text("DB_PASSWORD", ""),
			Name:            reader.text("DB_NAME", "mindos"),
			SSLMode:         reader.text("DB_SSLMODE", "disable"),
			MaxOpenConns:    reader.integer("DB_MAX_OPEN_CONNS", 25),
			MaxIdleConns:    reader.integer("DB_MAX_IDLE_CONNS", 10),
			ConnMaxLifetime: reader.duration("DB_CONN_MAX_LIFETIME", 30*time.Minute),
			PingTimeout:     reader.duration("DB_PING_TIMEOUT", 3*time.Second),
			LogSQL:          reader.boolean("DB_LOG_SQL", env == "dev"),
		},
		Redis: RedisConfig{
			Addr:     reader.text("REDIS_ADDR", "localhost:6379"),
			Password: reader.text("REDIS_PASSWORD", ""),
			DB:       reader.integer("REDIS_DB", 0),
			PoolSize: reader.integer("REDIS_POOL_SIZE", 20),
		},
		Storage: StorageConfig{
			Dir:            reader.text("STORAGE_DIR", "./storage"),
			MaxUploadBytes: reader.integer64("STORAGE_MAX_UPLOAD_BYTES", 10<<20),
		},
		LLM: LLMConfig{
			APIBase: strings.TrimRight(reader.text("LLM_API_BASE", ""), "/"),
			APIKey:  reader.text("LLM_API_KEY", ""),
			Model:   reader.text("LLM_MODEL", ""),
		},
		Embedding: EmbeddingConfig{
			APIBase: strings.TrimRight(reader.text("EMBEDDING_API_BASE", ""), "/"),
			APIKey:  reader.text("EMBEDDING_API_KEY", ""),
			Model:   reader.text("EMBEDDING_MODEL", ""),
		},
		Agent: AgentConfig{
			MaxSteps:    reader.integer("AGENT_MAX_STEPS", 30),
			TokenBudget: reader.integer64("AGENT_TOKEN_BUDGET", 200000),
			RunTimeout:  reader.duration("AGENT_RUN_TIMEOUT", 5*time.Minute),
		},
	}

	if err := reader.err(); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
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
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Errorf("LOG_LEVEL must be debug, info, warn or error, got %q", c.Log.Level))
	}
	switch c.Log.Format {
	case "console", "json":
	default:
		errs = append(errs, fmt.Errorf("LOG_FORMAT must be console or json, got %q", c.Log.Format))
	}
	if c.App.ShutdownTimeout <= 0 {
		errs = append(errs, errors.New("SHUTDOWN_TIMEOUT must be greater than 0"))
	}
	if c.DB.Host == "" {
		errs = append(errs, errors.New("DB_HOST is required"))
	}
	if c.DB.Port < 1 || c.DB.Port > 65535 {
		errs = append(errs, fmt.Errorf("DB_PORT must be between 1 and 65535, got %d", c.DB.Port))
	}
	if c.DB.Password == "" {
		errs = append(errs, errors.New("DB_PASSWORD is required"))
	}
	if c.DB.MaxOpenConns < 1 {
		errs = append(errs, fmt.Errorf("DB_MAX_OPEN_CONNS must be greater than 0, got %d", c.DB.MaxOpenConns))
	}
	if c.DB.MaxIdleConns < 0 {
		errs = append(errs, fmt.Errorf("DB_MAX_IDLE_CONNS must not be negative, got %d", c.DB.MaxIdleConns))
	}
	if c.DB.PingTimeout <= 0 {
		errs = append(errs, errors.New("DB_PING_TIMEOUT must be greater than 0"))
	}
	if strings.TrimSpace(c.Storage.Dir) == "" {
		errs = append(errs, errors.New("STORAGE_DIR must not be empty"))
	}
	if c.Storage.MaxUploadBytes < 1 {
		errs = append(errs, fmt.Errorf(
			"STORAGE_MAX_UPLOAD_BYTES must be greater than 0, got %d", c.Storage.MaxUploadBytes))
	}
	errs = append(errs, validateModelConfig("LLM", c.LLM.APIBase, c.LLM.APIKey, c.LLM.Model)...)
	errs = append(errs, validateModelConfig("EMBEDDING", c.Embedding.APIBase, c.Embedding.APIKey, c.Embedding.Model)...)
	if c.Agent.MaxSteps < 1 {
		errs = append(errs, fmt.Errorf("AGENT_MAX_STEPS must be at least 1, got %d", c.Agent.MaxSteps))
	}
	if c.Agent.TokenBudget < 1 {
		errs = append(errs, fmt.Errorf("AGENT_TOKEN_BUDGET must be at least 1, got %d", c.Agent.TokenBudget))
	}
	if c.Agent.RunTimeout <= 0 {
		errs = append(errs, errors.New("AGENT_RUN_TIMEOUT must be greater than 0"))
	}
	// errors.Join 是 Go 1.20 加入的：把多个错误合成一个，报错时一次看全。
	return errors.Join(errs...)
}

// validateModelConfig 检查模型配置的"全配或全空"规则。
//
// 三个字段配了一半（例如只填了 Key 没填 Model）说明配置过程被中断，
// 调用模型时才会暴露——那是 Phase 1 确立的"填错了带病启动"的同类问题，
// 所以在启动时就拒绝。全部为空是合法的：用户可能还没准备模型。
func validateModelConfig(prefix, apiBase, apiKey, model string) []error {
	set := map[string]bool{
		prefix + "_API_BASE": apiBase != "",
		prefix + "_API_KEY":  apiKey != "",
		prefix + "_MODEL":    model != "",
	}
	count := 0
	for _, v := range set {
		if v {
			count++
		}
	}
	if count == 0 || count == 3 {
		return nil
	}
	var missing []string
	for name, v := range set {
		if !v {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return []error{fmt.Errorf(
		"%s model config must be all set or all empty, missing: %s",
		prefix, strings.Join(missing, ", "))}
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

// envReader 读取环境变量，并记录类型解析错误。
//
// 为什么不让每个 getEnvXxx 直接返回 error：Load 里有十多个配置项，逐项写
// `if err != nil { return ... }` 会让主流程被错误处理切碎，而且第一个错误就返回，
// 用户要改一次、重启一次才能发现下一个。这里把错误暂存在局部 reader 中，最后用
// errors.Join 一次报全。
//
// 注意：envReader 只在 Load 内部创建，不保存配置值，也不被并发共享。
// 它收集的是"用户把配置写错了"，不是业务错误，所以错误信息可以包含配置项名和
// 非敏感的原值；密码等敏感项只用 text 读取，不进入这些解析错误。
type envReader struct {
	errs []error
}

// text 返回字符串配置。未设置、空串或只有空白时使用默认值。
// 密码等值保留原始内容，不做 TrimSpace，避免改变用户真实密码。
func (r *envReader) text(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}

// integer 读取整数。只有"没填"才回退；填了但不合法会记录错误，阻止服务启动。
func (r *envReader) integer(key string, fallback int) int {
	return int(r.integer64(key, int64(fallback)))
}

// integer64 与 integer 相同，但用于必须按 int64 记账的字段（如 token 预算）。
// 单独成方法而不是各处 int64(x)：转换集中在解析层，业务层拿到的类型就是最终类型。
func (r *envReader) integer64(key string, fallback int64) int64 {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return fallback
	}
	v = strings.TrimSpace(v)
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		r.errs = append(r.errs, fmt.Errorf("invalid %s: must be an integer, got %q", key, v))
		return fallback
	}
	return n
}

// boolean 只接受常见标准写法，不把 "maybe" 之类静默当成 false。
func (r *envReader) boolean(key string, fallback bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return fallback
	}
	v = strings.TrimSpace(v)
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		r.errs = append(r.errs, fmt.Errorf(
			"invalid %s: must be one of 1/0, true/false, yes/no, on/off, got %q",
			key, v,
		))
		return fallback
	}
}

// duration 读取 duration 字符串，必须带单位（如 10s、500ms、2h）。
// 只写 "10" 会被拒绝，因为 10 纳秒显然不是用户想表达的值。
func (r *envReader) duration(key string, fallback time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return fallback
	}
	v = strings.TrimSpace(v)
	d, err := time.ParseDuration(v)
	if err != nil {
		r.errs = append(r.errs, fmt.Errorf(
			"invalid %s: must be a duration such as 10s or 500ms, got %q",
			key, v,
		))
		return fallback
	}
	return d
}

// err 把解析阶段记录的所有错误合并成一个错误；没有错误时返回 nil。
func (r *envReader) err() error {
	return errors.Join(r.errs...)
}
