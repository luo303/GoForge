package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func newTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/status", nil)
	c.Request = req
	return c, rec
}

// statusBody 把响应解析成 map，测试断言用路径访问（如 data.postgres.status），
// 与前端实际消费的 JSON 结构保持一致。
func statusBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v\nbody: %s", err, rec.Body.String())
	}
	data, ok := body["data"].(map[string]any)
	if !ok {
		t.Fatalf("response has no data object: %s", rec.Body.String())
	}
	return data
}

func TestSystemStatusAllUp(t *testing.T) {
	h := NewSystemHandler(
		"test-version",
		func(ctx context.Context) error { return nil },
		func(ctx context.Context) (bool, error) { return true, nil },
		func(ctx context.Context) (MigrationInfo, error) {
			return MigrationInfo{Current: 1, Latest: 1}, nil
		},
		func(ctx context.Context) (bool, error) { return true, nil },
	)
	c, rec := newTestContext()
	h.Status(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200", rec.Code)
	}
	data := statusBody(t, rec)
	if data["version"] != "test-version" {
		t.Errorf("version = %v", data["version"])
	}
	if got := data["postgres"].(map[string]any)["status"]; got != "up" {
		t.Errorf("postgres.status = %v, want up", got)
	}
	if got := data["pgvector"].(map[string]any)["status"]; got != "up" {
		t.Errorf("pgvector.status = %v, want up", got)
	}
	if got := data["storage"].(map[string]any)["status"]; got != "up" {
		t.Errorf("storage.status = %v, want up", got)
	}
	mig := data["migration"].(map[string]any)
	if mig["state"] != "ok" {
		t.Errorf("migration.state = %v, want ok", mig["state"])
	}
}

func TestSystemStatusLocatesFailures(t *testing.T) {
	// 四项分别坏掉时，接口要能各自报出、互不影响——这是该接口存在的意义。
	cases := []struct {
		name          string
		dbPing        PingFunc
		vector        func(ctx context.Context) (bool, error)
		migration     func(ctx context.Context) (MigrationInfo, error)
		storage       StorageCheckFunc
		wantPostgres  string
		wantVector    string
		wantMigration string
		wantStorage   string
	}{
		{
			name:          "postgres down",
			dbPing:        func(ctx context.Context) error { return errors.New("connection refused") },
			vector:        func(ctx context.Context) (bool, error) { return false, errors.New("db down") },
			migration:     func(ctx context.Context) (MigrationInfo, error) { return MigrationInfo{}, errors.New("db down") },
			storage:       func(ctx context.Context) (bool, error) { return true, nil },
			wantPostgres:  "down",
			wantVector:    "unknown",
			wantMigration: "unknown",
			wantStorage:   "up",
		},
		{
			name:          "vector missing",
			dbPing:        func(ctx context.Context) error { return nil },
			vector:        func(ctx context.Context) (bool, error) { return false, nil },
			migration:     func(ctx context.Context) (MigrationInfo, error) { return MigrationInfo{Current: 1, Latest: 1}, nil },
			storage:       func(ctx context.Context) (bool, error) { return true, nil },
			wantPostgres:  "up",
			wantVector:    "missing",
			wantMigration: "ok",
			wantStorage:   "up",
		},
		{
			name:          "migration behind",
			dbPing:        func(ctx context.Context) error { return nil },
			vector:        func(ctx context.Context) (bool, error) { return true, nil },
			migration:     func(ctx context.Context) (MigrationInfo, error) { return MigrationInfo{Current: 1, Latest: 3}, nil },
			storage:       func(ctx context.Context) (bool, error) { return true, nil },
			wantPostgres:  "up",
			wantVector:    "up",
			wantMigration: "behind",
			wantStorage:   "up",
		},
		{
			name:   "migration dirty",
			dbPing: func(ctx context.Context) error { return nil },
			vector: func(ctx context.Context) (bool, error) { return true, nil },
			migration: func(ctx context.Context) (MigrationInfo, error) {
				return MigrationInfo{Current: 2, Latest: 2, Dirty: true}, nil
			},
			storage:       func(ctx context.Context) (bool, error) { return true, nil },
			wantPostgres:  "up",
			wantVector:    "up",
			wantMigration: "dirty",
			wantStorage:   "up",
		},
		{
			name:          "never migrated",
			dbPing:        func(ctx context.Context) error { return nil },
			vector:        func(ctx context.Context) (bool, error) { return true, nil },
			migration:     func(ctx context.Context) (MigrationInfo, error) { return MigrationInfo{Current: 0, Latest: 1}, nil },
			storage:       func(ctx context.Context) (bool, error) { return true, nil },
			wantPostgres:  "up",
			wantVector:    "up",
			wantMigration: "not_initialized",
			wantStorage:   "up",
		},
		{
			// MinIO 挂了但数据库还好：上传会 500，诊断接口必须能指出是存储这一侧。
			name:          "storage down",
			dbPing:        func(ctx context.Context) error { return nil },
			vector:        func(ctx context.Context) (bool, error) { return true, nil },
			migration:     func(ctx context.Context) (MigrationInfo, error) { return MigrationInfo{Current: 1, Latest: 1}, nil },
			storage:       func(ctx context.Context) (bool, error) { return false, errors.New("dial tcp: connection refused") },
			wantPostgres:  "up",
			wantVector:    "up",
			wantMigration: "ok",
			wantStorage:   "down",
		},
		{
			// 能连上但 bucket 不在了：不是网络故障，状态要和 down 区分开。
			name:          "storage bucket missing",
			dbPing:        func(ctx context.Context) error { return nil },
			vector:        func(ctx context.Context) (bool, error) { return true, nil },
			migration:     func(ctx context.Context) (MigrationInfo, error) { return MigrationInfo{Current: 1, Latest: 1}, nil },
			storage:       func(ctx context.Context) (bool, error) { return false, nil },
			wantPostgres:  "up",
			wantVector:    "up",
			wantMigration: "ok",
			wantStorage:   "missing",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewSystemHandler("v", tc.dbPing, tc.vector, tc.migration, tc.storage)
			c, rec := newTestContext()
			h.Status(c)

			if rec.Code != http.StatusOK {
				t.Fatalf("status code = %d, want 200 even when dependencies fail", rec.Code)
			}
			data := statusBody(t, rec)
			if got := data["postgres"].(map[string]any)["status"]; got != tc.wantPostgres {
				t.Errorf("postgres.status = %v, want %s", got, tc.wantPostgres)
			}
			if got := data["pgvector"].(map[string]any)["status"]; got != tc.wantVector {
				t.Errorf("pgvector.status = %v, want %s", got, tc.wantVector)
			}
			if got := data["migration"].(map[string]any)["state"]; got != tc.wantMigration {
				t.Errorf("migration.state = %v, want %s", got, tc.wantMigration)
			}
			if got := data["storage"].(map[string]any)["status"]; got != tc.wantStorage {
				t.Errorf("storage.status = %v, want %s", got, tc.wantStorage)
			}
		})
	}
}
