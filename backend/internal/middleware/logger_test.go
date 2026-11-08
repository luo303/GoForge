// Logger 中间件测试：固定"状态码 → 日志级别"这个容易在重构中被改坏的约定。
//
// 这里用了 zap 的 observer（zaptest/observer）：它把日志写进内存，
// 测试可以断言"打了几条、什么级别、带了哪些字段"，而不需要解析文本输出。
package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestLoggerLevelsByStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name      string
		status    int
		wantLevel zapcore.Level
	}{
		{name: "2xx is info", status: http.StatusOK, wantLevel: zapcore.InfoLevel},
		{name: "4xx is warn", status: http.StatusNotFound, wantLevel: zapcore.WarnLevel},
		{name: "5xx is error", status: http.StatusInternalServerError, wantLevel: zapcore.ErrorLevel},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			core, recorded := observer.New(zapcore.DebugLevel)
			log := zap.New(core)

			e := gin.New()
			e.Use(func(c *gin.Context) { c.Set("request_id", "test-rid") }, Logger(log))
			e.GET("/probe", func(c *gin.Context) { c.Status(tc.status) })

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/probe", nil)
			e.ServeHTTP(w, req)

			entries := recorded.All()
			if len(entries) != 1 {
				t.Fatalf("got %d log entries, want 1", len(entries))
			}
			if entries[0].Level != tc.wantLevel {
				t.Fatalf("level = %s, want %s", entries[0].Level, tc.wantLevel)
			}
			if got := entries[0].ContextMap()["request_id"]; got != "test-rid" {
				t.Fatalf("request_id = %v, want test-rid", got)
			}
		})
	}
}
