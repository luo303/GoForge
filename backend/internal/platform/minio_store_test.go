package platform

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"testing"

	"github.com/luo303/mindos/backend/internal/config"
)

// key 规则是两个实现之间的契约：documents.storage_path 里存的就是它，
// 换驱动后老数据能不能读出来，全看两边是否完全一致。
func TestDocumentKeyLayout(t *testing.T) {
	hash := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	want := "documents/ab/cd/" + hash
	if got := documentKey(hash); got != want {
		t.Fatalf("documentKey = %q, want %q", got, want)
	}
}

func TestLocalStoreUsesSharedKeyLayout(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	data := []byte("# 一致性\n\nkey 规则必须与对象存储相同。\n")

	saved, err := store.Save(context.Background(), data)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if want := documentKey(ContentHash(data)); saved.RelPath != want {
		t.Fatalf("rel path = %q, want %q", saved.RelPath, want)
	}
}

func TestValidateKey(t *testing.T) {
	cases := []struct {
		key string
		ok  bool
	}{
		{"documents/ab/cd/hash", true},
		{"documents/a.md", true},
		{"", false},
		{"/etc/passwd", false},
		{"documents/../secret", false},
	}
	for _, c := range cases {
		err := validateKey(c.key)
		if c.ok && err != nil {
			t.Errorf("validateKey(%q) = %v, want nil", c.key, err)
		}
		if !c.ok && err == nil {
			t.Errorf("validateKey(%q) = nil, want error", c.key)
		}
	}
}

// MinIO 集成测试：本地起 MinIO 后用环境变量打开，用法与 repository 的
// MINDOS_TEST_DSN 一致——没有真服务就跳过，不把"没装 MinIO"变成红灯。
//
//	MINDOS_TEST_MINIO_ENDPOINT=localhost:9000 \
//	MINDOS_TEST_MINIO_ACCESS_KEY=minioadmin \
//	MINDOS_TEST_MINIO_SECRET_KEY=minioadmin \
//	go test ./internal/platform -run TestMinIOStoreIntegration -v
func TestMinIOStoreIntegration(t *testing.T) {
	endpoint := os.Getenv("MINDOS_TEST_MINIO_ENDPOINT")
	if endpoint == "" {
		t.Skip("MINDOS_TEST_MINIO_ENDPOINT 未设置，跳过 MinIO 集成测试")
	}
	cfg := config.MinIOConfig{
		Endpoint:  endpoint,
		AccessKey: os.Getenv("MINDOS_TEST_MINIO_ACCESS_KEY"),
		SecretKey: os.Getenv("MINDOS_TEST_MINIO_SECRET_KEY"),
		Bucket:    os.Getenv("MINDOS_TEST_MINIO_BUCKET"),
	}
	if cfg.Bucket == "" {
		cfg.Bucket = "mindos-test"
	}

	ctx := context.Background()
	store, err := NewMinIOStore(ctx, cfg)
	if err != nil {
		t.Fatalf("new minio store: %v", err)
	}

	data := []byte("# MinIO 集成测试\n\n内容寻址与错误映射。\n")

	// 先把同名对象清掉：Remove 对不存在的对象也返回成功，
	// 这样重复跑测试时 first.Reused 的断言才有意义。
	key := documentKey(ContentHash(data))
	if err := store.Remove(ctx, key); err != nil {
		t.Fatalf("cleanup before save: %v", err)
	}

	first, err := store.Save(ctx, data)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if first.Reused {
		t.Fatalf("clean bucket 上首次写入不应标记 Reused")
	}
	if first.RelPath != key {
		t.Fatalf("rel path = %q, want %q", first.RelPath, key)
	}

	second, err := store.Save(ctx, data)
	if err != nil {
		t.Fatalf("save twice: %v", err)
	}
	if !second.Reused {
		t.Fatalf("相同内容第二次写入应标记 Reused")
	}
	if second.RelPath != first.RelPath {
		t.Fatalf("rel path = %q, want %q", second.RelPath, first.RelPath)
	}

	got, err := store.Read(ctx, first.RelPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("read = %q, want %q", got, data)
	}

	if err := store.Remove(ctx, first.RelPath); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := store.Read(ctx, first.RelPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("read after remove = %v, want fs.ErrNotExist", err)
	}
	// 删不存在的对象视为成功，与本地实现一致
	if err := store.Remove(ctx, first.RelPath); err != nil {
		t.Fatalf("remove missing object = %v, want nil", err)
	}
}
