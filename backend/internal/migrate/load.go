// 本文件负责从文件系统读取并解析迁移文件。
//
// 文件命名约定（与 golang-migrate 等工具保持一致，方便以后迁移到现成工具）：
//
//	migrations/0001_enable_pgvector.up.sql    正向变更
//	migrations/0001_enable_pgvector.down.sql  反向变更
//
// 规则：版本号是下划线前的十进制正整数；同一个版本必须同时存在 up 与 down
// 两个文件——只写 up 不写 down 的迁移在生产上是不可回滚的，这里在加载阶段
// 就拒绝，而不是等到回滚那天才报错。
package migrate

import (
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

// migration 是一条已解析的迁移：版本号 + 两个方向的 SQL 内容。
type migration struct {
	version int
	up      string
	down    string
}

// load 读取 fsys 根目录下所有迁移文件，按版本号升序返回。
// fsys 用 fs.FS 抽象而不是直接读目录：生产环境传 embed.FS（编译期打包），
// 测试传 fstest.MapFS（内存构造），两边走同一份解析逻辑。
func load(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}

	ups := map[int]string{}
	downs := map[int]string{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		// 非 .sql 文件（README、.gitkeep 之类）直接忽略；
		// 只有"声称是迁移但命名不对"的 .sql 文件才报错。
		if !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, direction, err := parseFilename(entry.Name())
		if err != nil {
			return nil, err
		}
		// 同一版本重复定义 up 或 down 是配置错误，直接失败比"后者覆盖前者"安全。
		if direction == "up" {
			if _, dup := ups[version]; dup {
				return nil, fmt.Errorf("duplicate up migration for version %d", version)
			}
			ups[version] = entry.Name()
		} else {
			if _, dup := downs[version]; dup {
				return nil, fmt.Errorf("duplicate down migration for version %d", version)
			}
			downs[version] = entry.Name()
		}
	}

	versions := make([]int, 0, len(ups))
	for v := range ups {
		versions = append(versions, v)
	}
	sort.Ints(versions)

	migs := make([]migration, 0, len(versions))
	for _, v := range versions {
		downName, ok := downs[v]
		if !ok {
			return nil, fmt.Errorf("migration %d has up file but no down file", v)
		}
		upSQL, err := fs.ReadFile(fsys, ups[v])
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", ups[v], err)
		}
		downSQL, err := fs.ReadFile(fsys, downName)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", downName, err)
		}
		migs = append(migs, migration{version: v, up: string(upSQL), down: string(downSQL)})
	}
	return migs, nil
}

// parseFilename 从文件名解析版本号与方向。
//
// 例："0001_enable_pgvector.up.sql" → version=1, direction="up"。
// 文件名格式非法、版本号不是正整数时返回错误——这类错误在编译/CI 阶段
// 就该暴露，而不是等到执行迁移时。
func parseFilename(name string) (version int, direction string, err error) {
	stem, ok := strings.CutSuffix(name, ".sql")
	if !ok {
		return 0, "", fmt.Errorf("migration file %q must end with .sql", name)
	}
	base, dir, ok := strings.Cut(stem, ".")
	if !ok || (dir != "up" && dir != "down") {
		return 0, "", fmt.Errorf("migration file %q must be named like 0001_name.up.sql", name)
	}
	numStr, _, ok := strings.Cut(base, "_")
	if !ok {
		return 0, "", fmt.Errorf("migration file %q must start with a version number followed by _", name)
	}
	version, err = strconv.Atoi(numStr)
	if err != nil || version <= 0 {
		return 0, "", fmt.Errorf("migration file %q has invalid version number %q", name, numStr)
	}
	return version, dir, nil
}
