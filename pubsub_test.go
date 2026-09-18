package pubsub

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestLocalDeliverExactAndWildcard 本地分发语义:精确命中 + 通配命中
func TestLocalDeliverExactAndWildcard(t *testing.T) {
	ps := New()
	var exactHits, wildHits atomic.Int32
	ps.Subscribe("user.login", func(e *Event) { exactHits.Add(1) })
	ps.Subscribe("user.>", func(e *Event) { wildHits.Add(1) })

	ps.Publish("user.login", nil)
	ps.Publish("user.logout", nil) //仅通配命中

	if v := exactHits.Load(); v != 1 {
		t.Fatalf("精确订阅应命中 1 次,拿到 %d", v)
	}
	if v := wildHits.Load(); v != 2 {
		t.Fatalf("通配订阅应命中 2 次,拿到 %d", v)
	}
}

// TestUnsubscribeStopsDelivery 取消订阅后不再投递;表项同步摘除
func TestUnsubscribeStopsDelivery(t *testing.T) {
	ps := New()
	var hits atomic.Int32
	ps.Subscribe("topic.a", func(e *Event) { hits.Add(1) })
	ps.Publish("topic.a", nil)
	ps.Unsubscribe("topic.a")
	ps.Publish("topic.a", nil)

	if v := hits.Load(); v != 1 {
		t.Fatalf("取消后不得再投递,实际 %d 次", v)
	}
	if n := ps.GetSubscriberCount("topic.a"); n != 0 {
		t.Fatalf("取消后订阅数应为 0,拿到 %d", n)
	}
	if n := len(ps.GetSubscriptions()); n != 0 {
		t.Fatalf("订阅表应为空,拿到 %d 项", n)
	}
}

// TestSubscribeConcurrentWithPublish 🔴判据回归:Subscribe/Unsubscribe(写侧,
// COW 重赋+handlers 换片)与 Publish/deliverLocal(读侧热路径)并发,
// 曾有裸字段读的竞争,-race 下必须干净
func TestSubscribeConcurrentWithPublish(t *testing.T) {
	ps := New()
	var received sync.WaitGroup
	var receivedCount atomic.Int32
	//预置一个订阅者,保证发布始终有落点
	ps.Subscribe("stable.topic", func(e *Event) { receivedCount.Add(1) })

	var wg sync.WaitGroup
	stop := make(chan struct{})
	//4 个写协程:在 churn.* 主题族上反复订阅/取消(不碰 stable.topic,
	//否则会把预置订阅者取消掉)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			topics := []string{"churn.*", "churn.exact", "other.>", "other.exact"}
			for j := 0; j < 200; j++ {
				tp := topics[j%len(topics)]
				ps.Subscribe(tp, func(e *Event) {})
				if j%3 == 0 {
					ps.Unsubscribe(tp)
				}
			}
		}(i)
	}
	//2 个发布协程:每个向 stable.topic 投递 300 次(预置订阅者应全收)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 300; j++ {
				ps.Publish("stable.topic", nil)
				ps.Publish("churn.exact", nil)
			}
		}()
	}
	wg.Wait()
	close(stop)
	received.Wait()

	//预置订阅者恰好收到 600 次:1(初始)+300*2(发布协程),一次不丢不重
	if v := receivedCount.Load(); v != 600 {
		t.Fatalf("预置订阅应恰好收到 600 次,拿到 %d", v)
	}
	//写侧结束后:预置订阅仍应在
	if n := ps.GetSubscriberCount("stable.topic"); n != 1 {
		t.Fatalf("原始订阅不得被其它协程误删,当前 %d", n)
	}
}

// fakeTransport 最小传输层:Start 记录 receiver,供测试直接注入远程消息
type fakeTransport struct {
	mu       sync.Mutex
	receiver func(topic string, data []byte)
}

func (f *fakeTransport) Start(receiver func(topic string, data []byte)) error {
	f.mu.Lock()
	f.receiver = receiver
	f.mu.Unlock()
	return nil
}
func (f *fakeTransport) Close() error                            { return nil }
func (f *fakeTransport) Publish(topic string, data []byte) error { return nil }
func (f *fakeTransport) Subscribe(topics []string)               {}
func (f *fakeTransport) Unsubscribe(topics []string)             {}

// TestRemoteReceiveQueued 远程路径:receive 只入队,dispatch 异步投递给订阅者。
// 没有 transport 时 Start 不起投递协程,必须有 transport 才走通这条路
func TestRemoteReceiveQueued(t *testing.T) {
	ps := New()
	ps.Use(&fakeTransport{})
	var hits atomic.Int32
	done := make(chan struct{})
	ps.Subscribe("remote.topic", func(e *Event) {
		if hits.Add(1) == 1 {
			close(done)
		}
	})
	if err := ps.Start(); err != nil {
		t.Fatalf("start error:%v", err)
	}
	defer ps.Close()

	ps.receive("remote.topic", []byte(`{}`))
	select {
	case <-done:
	case <-timeoutAfter(2):
		t.Fatal("远程消息未在超时内投递到订阅者")
	}
}

// timeoutAfter 秒级超时辅助(避免引 time 包名与测试语义混淆)
func timeoutAfter(seconds int64) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		<-time.After(time.Duration(seconds) * time.Second)
		close(ch)
	}()
	return ch
}
