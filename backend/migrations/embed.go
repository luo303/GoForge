// Package migrations 把 SQL 迁移文件通过 embed 打进二进制。
//
// 为什么用 embed：server 的 /system/status 需要知道"代码里要求的最新迁移版本"，
// 才能判断数据库是否落后。如果依赖运行时去读磁盘目录，工作目录一变就读不到；
// embed 在编译期把文件内容固定在二进制里，server 与 migrate 命令看到同一份。
package migrations

import "embed"

// FS 包含 migrations/ 目录下全部 *.sql 文件。
//
//go:embed *.sql
var FS embed.FS
