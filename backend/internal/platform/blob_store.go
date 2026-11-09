// 本文件定义"原始文档存哪里"的抽象与共用工具。
//
// 存储只有一种实现：MinIO（见 minio_store.go）。不再保留本地目录实现，
// 也不留成可切换的配置项——原文是重索引、重算 embedding、回溯引用的唯一依据，
// 一旦跟应用进程的磁盘绑在一起，容器重建、换机器、多实例部署都会变成
// "库里有记录但原文没了"。留成"可选项"意味着有人能选错，而选错的代价
// 往往在几周后要重索引时才显现。
//
// 接口（BlobStore）仍然保留，但目的不是"支持第二种实现"，而是给测试一个替身：
// knowledge 包的单测不该依赖一个跑着的 MinIO。Go 的隐式实现让两边不必
// import 对方的接口定义——谁用谁声明，方法集对上就算实现。
package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"strings"
)

// BlobStore 是原始文档存储的最小接口：写入、读回、删除。
//
// 方法里只出现 key 与 []byte，不出现对象句柄：
// 上传上限 10 MiB 且解析阶段本来就需要完整正文，流式接口换不来收益，
// 反而让实现与测试替身都要多写一层包装。
type BlobStore interface {
	Save(ctx context.Context, data []byte) (SavedFile, error)
	Read(ctx context.Context, key string) ([]byte, error)
	Remove(ctx context.Context, key string) error
}

// SavedFile 是一次写入的结果。
type SavedFile struct {
	// RelPath 是对象 key，入库存 documents.storage_path。
	RelPath string
	// Hash 是 SHA-256 的十六进制小写串（64 字符），入库存 documents.content_hash。
	Hash string
	Size int64
	// Reused 为 true 表示这份内容此前已经存在于存储中，本次没有写入新对象。
	// 导入失败回滚时靠它判断"能不能删"：复用来的对象属于更早的那条文档，
	// 删掉会让那条文档失去原文。
	Reused bool
}

// ContentHash 返回内容的 SHA-256 十六进制小写串。
//
// 单独导出是因为"笔记"也要算同一个哈希：documents.content_hash 的唯一索引
// 必须对 file 与 note 用同一种算法，否则同一段内容以两种来源进来
// 会被当成两条文档。
func ContentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// documentKey 返回内容寻址的对象 key：documents/<hash前2位>/<hash次2位>/<hash>。
//
// 二级前缀在对象存储里没有性能意义（S3 只有前缀扫描，没有目录概念），
// 但**不能随便改**：documents.storage_path 存的就是它，改了等于让所有
// 历史文档失去原文。它同时也是"按哈希前缀巡检对象"时的自然分组。
func documentKey(hash string) string {
	return path.Join("documents", hash[:2], hash[2:4], hash)
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
