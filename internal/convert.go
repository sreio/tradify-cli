package internal

import (
	"fmt"
	"strings"
	"sync"
	"unicode"

	"github.com/longbridgeapp/opencc"
)

// OpenCC 单例池
var (
	ccMu   sync.Mutex
	ccPool = map[string]*opencc.OpenCC{} // key: to (s2twp等)
)

// GetConverter 获取/初始化指定转换配置的 OpenCC 实例
func GetConverter(to string) (*opencc.OpenCC, error) {
	ccMu.Lock()
	defer ccMu.Unlock()
	if c, ok := ccPool[to]; ok && c != nil {
		return c, nil
	}
	c, err := opencc.New(to)
	if err != nil {
		return nil, fmt.Errorf("init opencc(%s): %w", to, err)
	}
	ccPool[to] = c
	return c, nil
}

// HasChinese 判断是否包含汉字
func HasChinese(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// IsASCIIOnly 是否仅包含 ASCII（0..127）
func IsASCIIOnly(s string) bool {
	if s == "" {
		return true
	}
	for _, r := range s {
		if r > 127 {
			return false
		}
	}
	return true
}

// ConvertIfNeeded 根据内容判断是否需要转换，避免不必要开销
func ConvertIfNeeded(to, in string) (string, bool, error) {
	if in == "" || IsASCIIOnly(in) || !HasChinese(in) {
		return in, false, nil
	}
	cc, err := GetConverter(to)
	if err != nil {
		return "", false, err
	}
	out, err := cc.Convert(in)
	if err != nil {
		return "", false, fmt.Errorf("opencc convert: %w", err)
	}
	if out == in {
		return in, false, nil
	}
	return out, true, nil
}

// SplitCSV 将逗号分隔字符串切分并清理空白
func SplitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
