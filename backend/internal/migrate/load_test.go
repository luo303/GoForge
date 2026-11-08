package migrate

import (
	"strings"
	"testing"
	"testing/fstest"
)

// mapFS 是构造内存迁移目录的辅助：每个文件一行内容，版本号从内容里
// 就能看出来，断言时不必再记版本与内容的对应关系。
func mapFS(files map[string]string) fstest.MapFS {
	m := fstest.MapFS{}
	for name, content := range files {
		m[name] = &fstest.MapFile{Data: []byte(content)}
	}
	return m
}

func TestLoadParsesAndSorts(t *testing.T) {
	fsys := mapFS(map[string]string{
		"0002_second.up.sql":   "second up",
		"0002_second.down.sql": "second down",
		"0001_first.up.sql":    "first up",
		"0001_first.down.sql":  "first down",
	})

	migs, err := load(fsys)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(migs) != 2 {
		t.Fatalf("got %d migrations, want 2", len(migs))
	}
	if migs[0].version != 1 || migs[0].up != "first up" || migs[0].down != "first down" {
		t.Errorf("migration[0] = %+v, want version 1 with first up/down", migs[0])
	}
	if migs[1].version != 2 || migs[1].up != "second up" || migs[1].down != "second down" {
		t.Errorf("migration[1] = %+v, want version 2 with second up/down", migs[1])
	}
}

func TestLoadRejectsMissingDown(t *testing.T) {
	fsys := mapFS(map[string]string{
		"0001_first.up.sql": "first up",
	})
	if _, err := load(fsys); err == nil {
		t.Fatal("load should fail when down file is missing")
	}
}

func TestLoadRejectsDuplicateVersion(t *testing.T) {
	fsys := mapFS(map[string]string{
		"0001_first.up.sql":   "a",
		"0001_first.down.sql": "a down",
		"0001_again.up.sql":   "b", // 与 0001 同版本
		"0001_again.down.sql": "b down",
	})
	if _, err := load(fsys); err == nil {
		t.Fatal("load should fail on duplicate up version")
	}
}

func TestLoadRejectsBadFilenames(t *testing.T) {
	cases := map[string]string{
		"no_version.up.sql":        "x", // 缺版本号
		"0000_zero.up.sql":         "x", // 版本号必须为正
		"0001_missing_dir.sql":     "x", // 缺 .up/.down
		"0001_no_underscoreup.sql": "x", // 版本后缺下划线
	}
	for name := range cases {
		fsys := mapFS(map[string]string{name: "x"})
		if _, err := load(fsys); err == nil {
			t.Errorf("load should reject %q", name)
		}
	}
}

func TestLoadAllowsNonSQLFilesIgnored(t *testing.T) {
	// README 之类的非 SQL 文件应被忽略而不是报错。
	fsys := mapFS(map[string]string{
		"README.md":           "docs",
		"0001_first.up.sql":   "up",
		"0001_first.down.sql": "down",
	})
	migs, err := load(fsys)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(migs) != 1 {
		t.Fatalf("got %d migrations, want 1", len(migs))
	}
}

// TestParseFilenameTable 用表驱动覆盖文件名解析的分支，比逐个写测试函数紧凑。
func TestParseFilenameTable(t *testing.T) {
	cases := []struct {
		name      string
		version   int
		direction string
		wantErr   bool
	}{
		{"0001_enable_pgvector.up.sql", 1, "up", false},
		{"0001_enable_pgvector.down.sql", 1, "down", false},
		{"12_x.up.sql", 12, "up", false},
		{"0000_x.up.sql", 0, "", true},
		{"abc_x.up.sql", 0, "", true},
		{"0001_x.sql", 0, "", true},
		{"0001_x.mid.sql", 0, "", true},
		{"0001_x.up.txt", 0, "", true},
	}
	for _, tc := range cases {
		v, dir, err := parseFilename(tc.name)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseFilename(%q) should fail", tc.name)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseFilename(%q): %v", tc.name, err)
			continue
		}
		if v != tc.version || dir != tc.direction {
			t.Errorf("parseFilename(%q) = (%d, %q), want (%d, %q)",
				tc.name, v, dir, tc.version, tc.direction)
		}
	}
}

// TestDirtyErrorMessage 确保 dirty 报错带版本号与恢复提示——
// 出问题时用户第一眼看到的信息决定恢复速度。
func TestDirtyErrorMessage(t *testing.T) {
	msg := dirtyError(7).Error()
	for _, want := range []string{"7", "dirty", "force"} {
		if !strings.Contains(msg, want) {
			t.Errorf("dirty error %q should mention %q", msg, want)
		}
	}
}
