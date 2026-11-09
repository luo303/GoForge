// 本文件是本地目录实现：上传的原始文档按内容哈希落盘。
//
// 为什么是"内容寻址"而不是自增文件名：
//  1. 同一个文件重复上传只会占一份磁盘，天然去重，不需要额外的清理任务；
//  2. 路径由内容决定，写入是幂等的——重试一次不会产生孤儿副本；
//  3. 与 documents.content_hash 唯一索引互为印证：路径里的哈希就是库里的哈希，
//     排查"库里有记录但文件没了"这类问题时不需要再查映射表。
//
// 落盘位置从 Phase 5.1 起降级为"无 Docker 时的调试选项"：默认走 MinIO
// （minio_store.go），因为原文是重索引的唯一依据，不该随应用容器的磁盘一起消失。
// 两个实现共用同一个 key 规则（documents/<前2位>/<次2位>/<hash>），
// 所以 documents.storage_path 在两个驱动之间通用——换驱动不需要改库。
package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// SavedFile 是一次落盘的结果。
type SavedFile struct {
	// RelPath 是相对存储根的路径，入库存 documents.storage_path。
	RelPath string
	// Hash 是 SHA-256 的十六进制小写串（64 字符），入库存 documents.content_hash。
	Hash string
	Size int64
	// Reused 为 true 表示这份内容此前已经存在于存储中，本次没有写入新文件。
	// 导入失败回滚时靠它判断"能不能删"：复用来的文件属于更早的那条文档，
	// 删掉会让那条文档失去原始文件。
	Reused bool
}

// FileStore 把文件写进一个目录，目录结构为 documents/<hash前2位>/<hash次2位>/<hash>。
// 二级前缀是为了避免单个目录里堆积上万个文件——大多数文件系统在目录项过万时
// 列目录会明显变慢。
type FileStore struct {
	root string
}

// NewFileStore 创建存储根目录（已存在则忽略）。
// 在启动时创建而不是等到第一次上传：目录不可写属于"部署错了"，
// 应该在启动日志里立刻暴露，而不是让用户上传时才看到 500。
func NewFileStore(dir string) (*FileStore, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve storage dir %q: %w", dir, err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("create storage dir %q: %w", abs, err)
	}
	return &FileStore{root: abs}, nil
}

// Root 返回存储根的绝对路径，供启动日志与 /settings 展示。
func (s *FileStore) Root() string { return s.root }

// ContentHash 返回内容的 SHA-256 十六进制小写串。
//
// 单独导出是因为"笔记"没有文件也要算同一个哈希：documents.content_hash
// 的唯一索引必须对 file 与 note 用同一种算法，否则同一段内容以两种来源
// 进来会被当成两条文档。
func ContentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Save 把整块内容写入存储，并返回内容哈希与相对路径。
//
// 参数是 []byte 而不是 io.Reader：上传上限由配置控制（默认 10 MiB），
// 解析阶段本来就需要完整正文（要按 rune 扫描、要校验 UTF-8），
// 多一次流式读取换不来内存收益，反而让"算哈希"和"写文件"必须分两趟读。
//
// 写入顺序：先写同目录临时文件，再 rename 到目标路径。
// rename 在同一文件系统内是原子的，因此不会出现"目标路径存在但内容只写了一半"；
// 直接往目标路径写，进程在写一半时被杀，会留下一个被后续请求当作"已存在"的坏文件。
func (s *FileStore) Save(ctx context.Context, data []byte) (SavedFile, error) {
	if err := ctx.Err(); err != nil {
		return SavedFile{}, err
	}

	hash := ContentHash(data)
	relPath := filepath.Join("documents", hash[:2], hash[2:4], hash)
	absPath := filepath.Join(s.root, relPath)

	// 内容寻址下"目标已存在"就等价于"同样内容已经在库里"，直接复用。
	if _, err := os.Stat(absPath); err == nil {
		return SavedFile{RelPath: relPath, Hash: hash, Size: int64(len(data)), Reused: true}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return SavedFile{}, fmt.Errorf("stat %s: %w", relPath, err)
	}

	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return SavedFile{}, fmt.Errorf("create %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return SavedFile{}, fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	// 成功路径会先 rename 再返回，此时 Remove 报 not exist 被忽略；
	// 失败路径（写失败、rename 失败）靠它保证不留临时文件。
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return SavedFile{}, fmt.Errorf("write temp file: %w", err)
	}
	// Close 的错误不能忽略：缓冲区里的数据是在 Close 时 flush 的，
	// 这里失败意味着落盘内容不完整。
	if err := tmp.Close(); err != nil {
		return SavedFile{}, fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, absPath); err != nil {
		return SavedFile{}, fmt.Errorf("rename into %s: %w", relPath, err)
	}

	return SavedFile{RelPath: relPath, Hash: hash, Size: int64(len(data))}, nil
}

// Read 读回一个由 Save 写出的文件，用于重索引。
//
// 不返回 os.ErrNotExist 的自定义包装：错误链里保留 fs.ErrNotExist 原始值，
// 调用方才能用 errors.Is 区分"文件被手工删了"（可解释的坏状态）
// 与"磁盘读失败"（真正的故障），两者的处置方式不同。
func (s *FileStore) Read(ctx context.Context, relPath string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !filepath.IsLocal(relPath) {
		return nil, fmt.Errorf("refuse to read path outside storage: %q", relPath)
	}
	data, err := os.ReadFile(filepath.Join(s.root, relPath))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", relPath, err)
	}
	return data, nil
}

// Remove 删除一个由 Save 写出的文件，用于入库失败后的补偿清理。
//
// 文件不存在视为成功：补偿清理是"尽力而为"的一步，它失败不该掩盖
// 真正的失败原因，调用方通常用 errors.Join 把两个错误一起返回。
func (s *FileStore) Remove(ctx context.Context, relPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// IsLocal 拒绝绝对路径与带 .. 的路径，避免将来某个调用方把
	// 用户输入直接当路径传进来时删到存储目录之外。这是纵深防御，不是当前有漏洞。
	if !filepath.IsLocal(relPath) {
		return fmt.Errorf("refuse to remove path outside storage: %q", relPath)
	}
	err := os.Remove(filepath.Join(s.root, relPath))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", relPath, err)
	}
	return nil
}
