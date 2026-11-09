package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestStore 用 t.TempDir 建一个隔离的存储根，测试之间互不影响。
func newTestStore(t *testing.T) *FileStore {
	t.Helper()
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	return store
}

func TestFileStoreSaveIsContentAddressed(t *testing.T) {
	store := newTestStore(t)
	content := []byte("# 标题\n\n正文内容\n")

	saved, err := store.Save(context.Background(), content)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	// 哈希必须是内容本身的 SHA-256，而不是随机名：库里的 content_hash 与
	// 磁盘路径靠它对应，两者不一致会让"重索引时找不到原文件"。
	sum := sha256.Sum256(content)
	if want := hex.EncodeToString(sum[:]); saved.Hash != want {
		t.Fatalf("Hash = %s, want %s", saved.Hash, want)
	}

	wantRel := filepath.Join("documents", saved.Hash[:2], saved.Hash[2:4], saved.Hash)
	if saved.RelPath != wantRel {
		t.Fatalf("RelPath = %s, want %s", saved.RelPath, wantRel)
	}
	if saved.Reused {
		t.Fatal("first save should not be marked as reused")
	}

	got, err := os.ReadFile(filepath.Join(store.Root(), saved.RelPath))
	if err != nil {
		t.Fatalf("read saved file: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("saved content = %q, want %q", got, content)
	}
}

func TestFileStoreSaveDeduplicates(t *testing.T) {
	store := newTestStore(t)
	content := []byte("同一份内容")

	first, err := store.Save(context.Background(), content)
	if err != nil {
		t.Fatalf("first Save: %v", err)
	}
	second, err := store.Save(context.Background(), content)
	if err != nil {
		t.Fatalf("second Save: %v", err)
	}

	if !second.Reused {
		t.Fatal("second save of identical content should be marked as reused")
	}
	if second.RelPath != first.RelPath {
		t.Fatalf("RelPath changed between identical saves: %s vs %s", first.RelPath, second.RelPath)
	}

	// 目录里只能有一个文件：残留的临时文件会在长期运行后把磁盘吃满。
	assertNoTempFiles(t, filepath.Join(store.Root(), "documents"))
}

func TestFileStoreRemove(t *testing.T) {
	store := newTestStore(t)
	saved, err := store.Save(context.Background(), []byte("删我"))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := store.Remove(context.Background(), saved.RelPath); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(store.Root(), saved.RelPath)); !os.IsNotExist(err) {
		t.Fatalf("file should be gone, stat err = %v", err)
	}
	// 重复删除视为成功：补偿清理可能被调用两次（例如事务错误与 defer 各一次）。
	if err := store.Remove(context.Background(), saved.RelPath); err != nil {
		t.Fatalf("Remove twice should be a no-op, got %v", err)
	}
}

func TestFileStoreRemoveRejectsEscapingPath(t *testing.T) {
	store := newTestStore(t)
	outside := filepath.Join(store.Root(), "..", "victim.txt")
	if err := os.WriteFile(outside, []byte("keep me"), 0o600); err != nil {
		t.Fatalf("prepare outside file: %v", err)
	}

	for _, p := range []string{"../victim.txt", "/etc/passwd", ""} {
		if err := store.Remove(context.Background(), p); err == nil {
			t.Fatalf("Remove(%q) should be rejected", p)
		}
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside file must survive, stat err = %v", err)
	}
}

func TestFileStoreReadRoundTrip(t *testing.T) {
	store := newTestStore(t)
	content := []byte("# 标题\n\n重索引要靠它把原文读回来。\n")

	saved, err := store.Save(context.Background(), content)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := store.Read(context.Background(), saved.RelPath)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("Read = %q, want %q", got, content)
	}

	// 文件不在时必须能判出来：调用方靠 errors.Is(err, fs.ErrNotExist)
	// 区分"原文丢了"与"磁盘故障"，两者的处置完全不同。
	if err := store.Remove(context.Background(), saved.RelPath); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := store.Read(context.Background(), saved.RelPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Read after remove: err = %v, want fs.ErrNotExist", err)
	}
}

func TestFileStoreReadRejectsEscapingPath(t *testing.T) {
	store := newTestStore(t)
	for _, p := range []string{"../secret.txt", "/etc/passwd", ""} {
		if _, err := store.Read(context.Background(), p); err == nil {
			t.Fatalf("Read(%q) should be rejected", p)
		}
	}
}

func TestFileStoreSaveHonorsCanceledContext(t *testing.T) {
	store := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := store.Save(ctx, []byte("不会落盘")); err == nil {
		t.Fatal("Save should fail when context is canceled")
	}
	assertNoTempFiles(t, filepath.Join(store.Root(), "documents"))
}

// assertNoTempFiles 断言目录树里没有 .upload-* 临时文件。
func assertNoTempFiles(t *testing.T, root string) {
	t.Helper()
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// 目录可能还没被创建（没有任何文件写入），这不是失败。
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !info.IsDir() && strings.HasPrefix(info.Name(), ".upload-") {
			t.Errorf("temporary file left behind: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
}
