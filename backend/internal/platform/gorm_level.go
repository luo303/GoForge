package platform

import (
	gormlogger "gorm.io/gorm/logger"

	"github.com/luo303/noema/backend/internal/config"
)

// gormLoggerLevel 决定 GORM 记录日志的详细程度。
//
// 开发环境把每条 SQL 都打出来，方便你观察 GORM 到底生成了什么 SQL
// （本项目的核心学习点之一：ORM 不是黑盒）；
// 生产环境只记录慢 SQL 和真实错误，避免日志量和 IO 成本失控。
func gormLoggerLevel(cfg config.DBConfig) gormlogger.LogLevel {
	if cfg.LogSQL {
		return gormlogger.Info
	}
	return gormlogger.Warn
}
