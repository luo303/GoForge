package platform

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// VectorExtensionPresent 检查当前数据库是否启用了 pgvector 扩展。
//
// 返回值区分两种"不算成功"的情况，这是 /system/status 能定位
// "扩展缺失"这一类问题的关键：
//   - (false, nil)：库能连，但扩展没装——属于"缺东西"，不是故障；
//   - (_, err)：数据库本身不可用——属于故障。
//
// 把这两种情况混在一起，状态接口就只能输出"down"，人无法判断该装扩展还是修连接。
func VectorExtensionPresent(ctx context.Context, db *gorm.DB) (bool, error) {
	if db == nil {
		return false, fmt.Errorf("database not initialized")
	}
	var exists bool
	err := db.WithContext(ctx).
		Raw("SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'vector')").
		Scan(&exists).Error
	if err != nil {
		return false, fmt.Errorf("check vector extension: %w", err)
	}
	return exists, nil
}
