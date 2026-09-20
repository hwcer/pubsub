package redis

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-redis/redis/v8"
)

// 本文件钉住统一 PSubscribe 单通道的两个关键行为:
// 1) 同一消息命中多个 pattern 时只投递一次(redis 按每命中 pattern 各投一份副本);
// 2) 连接被杀后监听协程自愈,不退出(旧实现任意 Receive 错误直接 return,永久失联)。

func newTestClient(t *testing.T) *redis.Client {
	t.Helper()
	//密码与 gateway/redis_test.go 的本机开发环境一致;无 redis/无密码环境自动跳过
	c := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379", Password: "123456"})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Ping(ctx).Err(); err != nil {
		t.Skipf("本地 redis 不可用,跳过集成测试:%v", err)
	}
	return c
}

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pattern, s string
		want       bool
	}{
		{"a.b", "a.b", true},
		{"a.b", "a.c", false},
		{"a.*", "a.b", true},
		{"a.*", "a.b.c", true}, //pattern() 的 * 是 redis glob,宽松匹配,由 deliverLocal 精确过滤
		{"a.*", "b.b", false},
		{"*", "anything.here", true},
		{"a.b", "a.bb", false},
	}
	for _, c := range cases {
		if got := globMatch(c.pattern, c.s); got != c.want {
			t.Errorf("globMatch(%q,%q)=%v, want %v", c.pattern, c.s, got, c.want)
		}
	}
}

func TestPatternExclusive(t *testing.T) {
	tr := New(nil, "t")
	tr.topics["a.b"] = true
	tr.topics["a.>"] = true
	//命中 a.b 的两个 pattern:t:a.* 与 t:a.b,字典序最小者为 t:a.*( '*' < 'b' )
	if !tr.patternExclusive("t:a.*", "t:a.b") {
		t.Fatal("字典序最小的命中者应放行")
	}
	if tr.patternExclusive("t:a.b", "t:a.b") {
		t.Fatal("非最小命中者的副本应被丢弃")
	}
	//无任何 pattern 匹配(未文档化的元字符等边界):放行,宁投递不丢消息
	if !tr.patternExclusive("t:zzz", "t:x.y") {
		t.Fatal("集合无命中时应放行兜底")
	}
}

// 同一消息命中两个 pattern(精确 + 通配),receiver 必须恰好收到一次
func TestDuplicateDeliveryDeduped(t *testing.T) {
	client := newTestClient(t)
	tr := New(client, "")
	defer tr.Close()
	topic := "test.pubsub.dedup"
	tr.Subscribe([]string{topic, "test.pubsub.>"})

	var count int32
	if err := tr.Start(func(string, []byte) {
		atomic.AddInt32(&count, 1)
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(300 * time.Millisecond) //等 psubscribe 确认生效

	env, err := json.Marshal(&envelope{Origin: "external", Data: json.RawMessage(`"x"`)})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.Publish(ctx, topic, string(env)).Err(); err != nil {
		t.Fatalf("publish: %v", err)
	}
	for atomic.LoadInt32(&count) == 0 && ctx.Err() == nil {
		time.Sleep(50 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&count); got != 1 {
		t.Fatalf("去重后应恰好投递 1 次,实际 %d 次", got)
	}
}

// 连接被杀后监听协程必须自愈:重连重订阅并继续收到消息
func TestListenReconnectsAfterConnKill(t *testing.T) {
	client := newTestClient(t)
	tr := New(client, "")
	defer tr.Close()
	topic := "test.pubsub.reconnect"
	tr.Subscribe([]string{topic})

	var got int32
	if err := tr.Start(func(string, []byte) {
		atomic.AddInt32(&got, 1)
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	ctx := context.Background()
	//杀掉本进程的 pubsub 连接(管理连接是 normal 类型不受影响);
	//旧实现在此处之后监听协程已退出,后面的消息永远收不到
	if err := client.Do(ctx, "CLIENT", "KILL", "TYPE", "pubsub").Err(); err != nil {
		t.Fatalf("client kill: %v", err)
	}
	time.Sleep(500 * time.Millisecond) //留出出错→退避→重连重订阅的时间

	env, err := json.Marshal(&envelope{Origin: "external", Data: json.RawMessage(`"x"`)})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	if err := client.Publish(ctx, topic, string(env)).Err(); err != nil {
		t.Fatalf("publish: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for atomic.LoadInt32(&got) == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&got); got == 0 {
		t.Fatal("连接被杀后应自动重连并收到消息(旧实现此处永久失联)")
	}
}
