package knowledge

import (
	"encoding/json"
	"reflect"
	"testing"
)

// 写出去的字面量必须能被读回来：Value / Scan 是一对互逆操作，
// 任何一边单独测试都发现不了"转义写错了但解析也错了"这类成对的 bug。
func TestStringArrayRoundTrip(t *testing.T) {
	cases := []StringArray{
		{},
		{"go"},
		{"go", "数据库"},
		{"含 空格", "含,逗号"},
		{`含"引号"`, `含\反斜杠`},
		{"含{花括号}", ""},
	}

	for _, in := range cases {
		value, err := in.Value()
		if err != nil {
			t.Fatalf("Value(%v): %v", in, err)
		}
		text, ok := value.(string)
		if !ok {
			t.Fatalf("Value(%v) returned %T, want string", in, value)
		}

		var out StringArray
		if err := out.Scan(text); err != nil {
			t.Fatalf("Scan(%q): %v", text, err)
		}
		if len(in) == 0 {
			if len(out) != 0 {
				t.Fatalf("round trip of empty array = %v", out)
			}
			continue
		}
		if !reflect.DeepEqual([]string(in), []string(out)) {
			t.Fatalf("round trip %v -> %q -> %v", in, text, out)
		}
	}
}

func TestParseArrayLiteral(t *testing.T) {
	cases := []struct {
		raw  string
		want StringArray
	}{
		{`{}`, StringArray{}},
		{`{go}`, StringArray{"go"}},
		{`{go,pgx}`, StringArray{"go", "pgx"}},
		{`{"go lang",pgx}`, StringArray{"go lang", "pgx"}},
		{`{"a,b"}`, StringArray{"a,b"}},
		{`{"say \"hi\""}`, StringArray{`say "hi"`}},
		{`{"back\\slash"}`, StringArray{`back\slash`}},
		{`{数据库,检索}`, StringArray{"数据库", "检索"}},
		{`{a, b}`, StringArray{"a", "b"}},
	}
	for _, c := range cases {
		got, err := parseArrayLiteral(c.raw)
		if err != nil {
			t.Fatalf("parseArrayLiteral(%q): %v", c.raw, err)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("parseArrayLiteral(%q) = %v, want %v", c.raw, got, c.want)
		}
	}
}

func TestParseArrayLiteralRejectsGarbage(t *testing.T) {
	for _, raw := range []string{"", "go", "{go", "go}"} {
		if _, err := parseArrayLiteral(raw); err == nil {
			t.Errorf("parseArrayLiteral(%q) should fail", raw)
		}
	}
}

func TestStringArrayScanRejectsUnknownType(t *testing.T) {
	var a StringArray
	if err := a.Scan(42); err == nil {
		t.Fatal("scanning an int should fail loudly instead of silently producing an empty array")
	}
}

// 底层类型是 []string，所以 JSON 序列化天然是数组——前端不需要为标签写任何转换。
func TestStringArrayJSONShape(t *testing.T) {
	raw, err := json.Marshal(StringArray{"go", "数据库"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(raw) != `["go","数据库"]` {
		t.Fatalf("json = %s", raw)
	}
}
