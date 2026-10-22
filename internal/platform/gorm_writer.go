package platform

import (
	"fmt"

	"go.uber.org/zap"
)

// zapWriter 实现 gorm.io/gorm/logger.Writer 接口：
//
//	type Writer interface {
//		Printf(string, ...interface{})
//	}
//
// GORM 内部只通过这一个方法输出日志，所以适配成本极低：
// 我们不需要重新实现整个 logger.Interface，只要把"往控制台写"变成"往 zap 写"。
type zapWriter struct {
	log *zap.Logger
}

// Printf 把 GORM 已经格式化好的整行日志交给 zap。
//
// 这里用 Info 而不是 Debug 是刻意的：是否输出 SQL 由 gormlogger.Config.LogLevel 决定，
// 能走到这里说明这条日志本来就该输出；若再用 Debug，
// 当 logger 级别为 info 时，GORM 的 Warn 级日志反而会被吞掉。
func (w zapWriter) Printf(format string, args ...any) {
	if w.log == nil {
		return
	}
	if len(args) == 0 {
		w.log.Info(format)
		return
	}
	w.log.Info(fmt.Sprintf(format, args...))
}
