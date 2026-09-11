package pubsub

import (
	"regexp"
	"strings"
	"sync"
)

var wildcardCache sync.Map // topic → *regexp.Regexp

func isWildcard(topic string) bool {
	return strings.ContainsAny(topic, "*>")
}

func compileWildcard(topic string) *regexp.Regexp {
	if v, ok := wildcardCache.Load(topic); ok {
		return v.(*regexp.Regexp)
	}
	pattern := "^" + regexp.QuoteMeta(topic) + "$"
	pattern = strings.ReplaceAll(pattern, `\*`, `[^.]+`)
	//🔴 QuoteMeta 不转义 `>`,模式里是裸 `>`,`\>` 替换是死代码——
	//`>` 通配曾整体失效(只能匹配字面量),改为替换裸字符
	pattern = strings.ReplaceAll(pattern, `>`, `.+`)
	re := regexp.MustCompile(pattern)
	wildcardCache.Store(topic, re)
	return re
}
