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
	pattern = strings.ReplaceAll(pattern, `\>`, `.+`)
	re := regexp.MustCompile(pattern)
	wildcardCache.Store(topic, re)
	return re
}
