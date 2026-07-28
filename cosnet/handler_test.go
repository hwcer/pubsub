package cosnet

import (
	"reflect"
	"strings"
	"testing"
)

// 路由是 Register(handler, basePath, "%m") 按方法名注册的，registry.Formatter
// 为 strings.ToLower，所以路径常量必须等于 basePath + "/" + 小写方法名。
// 曾经 pathBatchSubscribe 写成 "/batch_subscribe"，与方法名 BatchSubscribe 对不上，
// 消息落到无 handler 的路径被静默丢弃：客户端连上了、订阅却永远同步不到服务端，
// 表现为"连接正常但一条消息都收不到"。
func TestPathMatchesMethodName(t *testing.T) {
	cases := []struct {
		handler any
		method  string
		path    string
	}{
		{&serverHandler{}, "Subscribe", pathSubscribe},
		{&serverHandler{}, "BatchSubscribe", pathBatchSubscribe},
		{&serverHandler{}, "Unsubscribe", pathUnsubscribe},
		{&serverHandler{}, "Publish", pathPublish},
		{&serverHandler{}, "Ping", pathPing},
		{&clientHandler{}, "Message", pathMessage},
		{&clientHandler{}, "Ping", pathPing},
	}
	for _, c := range cases {
		if _, ok := reflect.TypeOf(c.handler).MethodByName(c.method); !ok {
			t.Errorf("%T 没有方法 %s，路径 %s 将无人处理", c.handler, c.method, c.path)
			continue
		}
		want := basePath + "/" + strings.ToLower(c.method)
		if c.path != want {
			t.Errorf("%T.%s 的路径应为 %s，实际 %s", c.handler, c.method, want, c.path)
		}
	}
}
