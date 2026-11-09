// 本文件解决一个具体问题：documents.tags 是 PostgreSQL 的 text[]，
// 而 GORM 对 []string 既不认类型也不会读写它。
//
// 两条实测结论（实验方式：拿真实 PostgreSQL 建一张 tags text[] 的表，
// 用 GORM 分别试 Create / Select）：
//
//	[]string + gorm:"type:text[]"  写入报 "expression is of type record"
//	                                （GORM 把切片拼成了行构造器 ('a','b')）
//	[]string 读取报 "unsupported Scan, storing driver.Value type string into *[]string"
//	                                （驱动给回来的是数组字面量字符串）
//
// 所以需要一个实现 driver.Valuer + sql.Scanner 的类型：写的时候产出 "{\"a\",\"b\"}"
// 这样的数组字面量，读的时候把字符串解析回切片。这也是 Go 里对接数据库自定义类型的
// 标准做法——不需要引入 pq.StringArray 这种额外依赖。
package knowledge

import (
	"database/sql/driver"
	"fmt"
	"strings"
)

// StringArray 是 text[] 与 []string 之间的桥。
//
// 底层类型是 []string，所以可以直接 len()/range，也能直接序列化成 JSON 数组
// （encoding/json 按底层类型处理，不需要额外实现 MarshalJSON）。
type StringArray []string

// Value 产出 PostgreSQL 数组字面量。
//
// 一律给每个元素加双引号：数组字面量里未加引号的元素遇到空格、逗号、花括号
// 都需要转义，而"永远加引号 + 转义引号和反斜杠"是唯一不需要分情况讨论的写法。
// 返回值用 string 而不是 []byte：实测 pgx 能直接把 string 编码成 text[] 参数，
// 不需要在 SQL 里额外写 ::text[] 强转。
func (a StringArray) Value() (driver.Value, error) {
	if len(a) == 0 {
		return "{}", nil
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, s := range a {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		b.WriteString(escapeArrayElement(s))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String(), nil
}

// Scan 把数据库返回值解析成切片。
//
// 实测 pgx 给回来的是 string；[]byte 分支保留是因为别的驱动（lib/pq、
// 以及 database/sql 的某些转换路径）会给 []byte，多写两行比以后换驱动时
// 冒出一个"读标签就 panic"的运行时错误划算。
func (a *StringArray) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*a = nil
		return nil
	case string:
		parsed, err := parseArrayLiteral(v)
		if err != nil {
			return err
		}
		*a = parsed
		return nil
	case []byte:
		parsed, err := parseArrayLiteral(string(v))
		if err != nil {
			return err
		}
		*a = parsed
		return nil
	case []string:
		*a = v
		return nil
	default:
		return fmt.Errorf("knowledge: cannot scan %T into StringArray", src)
	}
}

// escapeArrayElement 转义数组元素里的反斜杠与双引号（PG 数组字面量的规则）。
func escapeArrayElement(s string) string {
	if !strings.ContainsAny(s, `\"`) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if r == '\\' || r == '"' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// parseArrayLiteral 解析 PostgreSQL 的数组文本格式：{a,b}、{"a b","c"}、{}。
//
// 只做"把数组读回来"这一件事，不支持嵌套数组与多维（tags 不会有这种数据）。
// 逐字节扫描是安全的：这里只比较 ASCII 分隔符与转义符，
// 多字节字符会被原样拷进结果，不会在中间被切开。
func parseArrayLiteral(s string) (StringArray, error) {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '{' || s[len(s)-1] != '}' {
		return nil, fmt.Errorf("knowledge: %q is not an array literal", s)
	}
	body := s[1 : len(s)-1]
	if strings.TrimSpace(body) == "" {
		return StringArray{}, nil
	}

	out := make(StringArray, 0, strings.Count(body, ",")+1)
	i := 0
	for i < len(body) {
		var elem strings.Builder
		if body[i] == '"' {
			i++
			for i < len(body) {
				c := body[i]
				if c == '\\' && i+1 < len(body) {
					elem.WriteByte(body[i+1])
					i += 2
					continue
				}
				if c == '"' {
					i++
					break
				}
				elem.WriteByte(c)
				i++
			}
		} else {
			for i < len(body) && body[i] != ',' {
				elem.WriteByte(body[i])
				i++
			}
		}
		out = append(out, elem.String())
		if i < len(body) && body[i] == ',' {
			i++
			// 逗号后允许空白，PG 一般不写，但手工拼的 SQL 可能有。
			for i < len(body) && body[i] == ' ' {
				i++
			}
		}
	}
	return out, nil
}
