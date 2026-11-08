package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/luo303/mindos/backend/internal/httpx"
	"github.com/luo303/mindos/backend/internal/middleware"
)

// TestPanicRequestStillWritesAccessLog 守住中间件装配顺序。
//
// 旧顺序 RequestID → Recovery → Logger 下，Handler panic 会跳过 Logger
// 写在 c.Next() 之后的访问日志；服务端虽然返回 500，却查不到这次请求。
// 这个测试必须通过真实的 newEngine 装配，不能自己另写一遍 Use(...)，
// 否则 main.go 的顺序被改坏时测试仍然会通过。
func TestPanicRequestStillWritesAccessLog(t *testing.T) {
	gin.SetMode(gin.TestMode)

	core, recorded := observer.New(zapcore.DebugLevel)
	log := zap.New(core)
	engine := newEngine(log, false)
	engine.GET("/panic", func(*gin.Context) {
		panic("test panic")
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/panic", nil)
	engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}

	accessLogs := recorded.FilterMessage("http request").All()
	if len(accessLogs) != 1 {
		t.Fatalf("got %d access logs, want 1", len(accessLogs))
	}
	if accessLogs[0].Level != zapcore.ErrorLevel {
		t.Fatalf("access log level = %s, want error", accessLogs[0].Level)
	}

	accessFields := accessLogs[0].ContextMap()
	if status, ok := accessFields["status"].(int64); !ok || status != http.StatusInternalServerError {
		t.Fatalf("access log status = %v, want 500", accessFields["status"])
	}
	requestID, _ := accessFields["request_id"].(string)
	if requestID == "" {
		t.Fatal("access log request_id is empty")
	}
	if got := recorder.Header().Get(middleware.RequestIDHeader); got != requestID {
		t.Fatalf("response request id = %q, log request id = %q", got, requestID)
	}

	panicLogs := recorded.FilterMessage("panic recovered").All()
	if len(panicLogs) != 1 {
		t.Fatalf("got %d panic logs, want 1", len(panicLogs))
	}
	if got := panicLogs[0].ContextMap()["request_id"]; got != requestID {
		t.Fatalf("panic log request_id = %v, want %s", got, requestID)
	}

	var envelope httpx.Envelope
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Code != httpx.ErrInternal.Code {
		t.Fatalf("response code = %q, want %q", envelope.Code, httpx.ErrInternal.Code)
	}
	if envelope.RequestID != requestID {
		t.Fatalf("response request_id = %q, want %q", envelope.RequestID, requestID)
	}
	if strings.Contains(recorder.Body.String(), "test panic") {
		t.Fatal("non-dev response must not expose panic details")
	}
}
