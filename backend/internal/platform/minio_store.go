// 本文件是 MinIO（S3 协议）实现：上传的原始文档按内容哈希写进 bucket。
//
// 这是 MindOS 唯一的原文存储实现，没有同级的"本地目录"备选：
// 原文是"重索引、重算 embedding、回溯引用"的唯一依据。它一旦跟应用进程的
// 磁盘绑在一起，容器重建、换机器、多实例部署都会变成"库里有记录但原文没了"。
// 对象存储把这个生命周期从进程里剥离出来，代价只是多一个必须常驻的容器。
//
// key 规则是 documents/<前2位>/<次2位>/<hash>，与 documents.storage_path
// 逐字对应：排查"库里有记录但对象没了"时，不需要再查任何映射表。
package platform

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/luo303/mindos/backend/internal/config"
)

// MinIOStore 把原文写进一个 bucket，对象名就是内容哈希的分层 key。
type MinIOStore struct {
	client   *minio.Client
	bucket   string
	endpoint string
}

// NewMinIOStore 建立连接并确保 bucket 存在。
//
// 启动时就建 bucket，而不是等第一次上传：bucket 不存在、凭据不对、端点不可达
// 都属于"部署错了"，应该在启动日志里立刻暴露。这也和项目原有的
// "配置错误不许带病启动"保持一致。
func NewMinIOStore(ctx context.Context, cfg config.MinIOConfig) (*MinIOStore, error) {
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("create minio client for %q: %w", cfg.Endpoint, err)
	}

	store := &MinIOStore{client: client, bucket: cfg.Bucket, endpoint: cfg.Endpoint}
	if err := store.ensureBucket(ctx); err != nil {
		return nil, err
	}
	return store, nil
}

// Endpoint 返回端点，供启动日志与 /settings 展示（不含凭据）。
func (s *MinIOStore) Endpoint() string { return s.endpoint }

// Bucket 返回 bucket 名，供启动日志与 /settings 展示。
func (s *MinIOStore) Bucket() string { return s.bucket }

// ensureBucket 建 bucket（已存在则跳过）。
func (s *MinIOStore) ensureBucket(ctx context.Context) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("check bucket %q on %s: %w", s.bucket, s.endpoint, err)
	}
	if exists {
		return nil
	}
	if err := s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{}); err != nil {
		// 并发启动时另一个实例可能刚好建好：再查一次，存在即视为成功，
		// 把"别人先建好了"这种正常竞争挡在错误之外。
		if again, checkErr := s.client.BucketExists(ctx, s.bucket); checkErr == nil && again {
			return nil
		}
		return fmt.Errorf("create bucket %q on %s: %w", s.bucket, s.endpoint, err)
	}
	return nil
}

// Save 把整块内容写进 bucket，返回内容哈希与对象 key。
//
// 幂等：先 StatObject，已存在就标记 Reused 不再上传。
// 并发同内容上传时两个请求可能都 Stat 不到，随后都 PutObject 同一个 key——
// 内容寻址下两次写入的字节完全相同，后写覆盖前写结果不变，所以这里不需要加锁。
func (s *MinIOStore) Save(ctx context.Context, data []byte) (SavedFile, error) {
	if err := ctx.Err(); err != nil {
		return SavedFile{}, err
	}

	hash := ContentHash(data)
	key := documentKey(hash)

	if _, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{}); err == nil {
		return SavedFile{RelPath: key, Hash: hash, Size: int64(len(data)), Reused: true}, nil
	} else if !isObjectNotFound(err) {
		return SavedFile{}, fmt.Errorf("stat object %s: %w", key, err)
	}

	// PutObjectOptions 显式给 ContentType：否则 MinIO 存成
	// application/octet-stream，将来做"原文下载"时浏览器一律弹出保存框。
	_, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: "text/plain; charset=utf-8"})
	if err != nil {
		return SavedFile{}, fmt.Errorf("put object %s: %w", key, err)
	}
	return SavedFile{RelPath: key, Hash: hash, Size: int64(len(data))}, nil
}

// Read 读回原文，用于重索引。
//
// 对象不存在时返回 fs.ErrNotExist：调用方（knowledge.sourceText）靠它区分
// "原文被删了"（可解释的坏状态，回 409）与"存储故障"（真正的错误，回 500）。
func (s *MinIOStore) Read(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateKey(key); err != nil {
		return nil, err
	}

	object, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("get object %s: %w", key, err)
	}
	defer func() { _ = object.Close() }()

	// minio 的 GetObject 是惰性的：网络错误、NoSuchKey 都在第一次读时才出现，
	// 不能只检查上面那个 err。
	data, err := io.ReadAll(object)
	if err != nil {
		if isObjectNotFound(err) {
			return nil, fmt.Errorf("read object %s: %w", key, fs.ErrNotExist)
		}
		return nil, fmt.Errorf("read object %s: %w", key, err)
	}
	return data, nil
}

// Remove 删除一个对象，用于删文档与入库失败后的补偿清理。
//
// S3 的 DELETE 本身幂等（对象不存在也返回成功），所以这里不需要额外判断，
// 与本地实现"文件不存在视为成功"的语义一致。
func (s *MinIOStore) Remove(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateKey(key); err != nil {
		return err
	}
	if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("remove object %s: %w", key, err)
	}
	return nil
}

// isObjectNotFound 判断错误是否为"对象不存在"。
//
// MinIO 与 S3 对同一件事返回两种形态：GetObject 读失败给 NoSuchKey，
// StatObject 有时只给 404 状态码。两个都认，避免"文件其实没了却当成存储故障"。
func isObjectNotFound(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	resp := minio.ToErrorResponse(err)
	return resp.Code == "NoSuchKey" || resp.StatusCode == http.StatusNotFound
}
