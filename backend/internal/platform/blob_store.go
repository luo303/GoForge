// 本文件定义"原始文档存哪里"的抽象与工厂。
//
// 为什么装配层需要这里的 BlobStore：cmd/server 要用一个变量同时装下本地目录
// 与对象存储两种实现，而 knowledge 包里的 fileStore 是未导出的。
// 两个接口方法集完全一样，靠 Go 的隐式实现天然兼容——接口由使用方声明，
// 谁用谁定义，不需要 import 对方，也不会因为多声明一个方法而互相牵制。
package platform

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/luo303/mindos/backend/internal/config"
)

// BlobStore 是原始文档存储的最小接口：写入、读回、删除。
//
// 方法里只出现 key 与 []byte，不出现 *os.File / 对象句柄：
// 上传上限 10 MiB 且解析阶段本来就需要完整正文，流式接口换不来收益，
// 反而让两个实现都要多写一层包装。
type BlobStore interface {
	Save(ctx context.Context, data []byte) (SavedFile, error)
	Read(ctx context.Context, key string) ([]byte, error)
	Remove(ctx context.Context, key string) error
}

// documentKey 返回内容寻址的存储 key：documents/<hash前2位>/<hash次2位>/<hash>。
//
// 二级前缀对本地目录有意义（避免单目录上万文件导致列目录变慢），
// 对对象存储没有性能意义，但**必须保持一致**：
// documents.storage_path 存的就是它，换驱动后老数据仍要能被读出来。
func documentKey(hash string) string {
	return path.Join("documents", hash[:2], hash[2:4], hash)
}

// NewBlobStore 按配置创建存储实现。
//
// 这里再判一次 driver 而不是依赖配置校验：工厂被别的调用方（测试、将来的
// 迁移命令）直接使用时，也应该拿到明确错误，而不是静默返回 nil。
func NewBlobStore(ctx context.Context, cfg config.StorageConfig) (BlobStore, error) {
	switch cfg.Driver {
	case "local":
		return NewFileStore(cfg.Dir)
	case "minio":
		return NewMinIOStore(ctx, cfg.MinIO)
	default:
		return nil, fmt.Errorf("unknown storage driver %q", cfg.Driver)
	}
}

// validateKey 拒绝空 key、绝对路径与带 .. 的 key。
//
// 对象存储里 key 只是字符串，`../x` 不会真的越界；但 storage_path 来自数据库，
// 将来也可能被人工改，宁可在这里拒绝，也不要让一个可疑 key 悄悄写进 bucket。
func validateKey(key string) error {
	if key == "" {
		return fmt.Errorf("storage key is empty")
	}
	if strings.HasPrefix(key, "/") || path.Clean(key) != key {
		return fmt.Errorf("invalid storage key %q", key)
	}
	return nil
}
