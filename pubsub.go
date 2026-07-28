package pubsub

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sync"
)

type subscription struct {
	Topic    string
	Pattern  *regexp.Regexp // 通配符预编译正则，精确匹配时为 nil
	Handlers []Handler
}

// DefaultQueueSize 远程消息队列的默认容量
const DefaultQueueSize = 256

// ErrQueueFull 远程消息队列已满，当前消息被丢弃
var ErrQueueFull = errors.New("pubsub: queue is full")

// PubSub 事件总线，支持本地发布/订阅和可插拔的远程传输层
// 使用 COW 模式优化读取性能
type PubSub struct {
	mutex      sync.Mutex
	transports []Transport
	exact      map[string]*subscription
	wildcards  []*subscription
	queue      chan *Event
	done       chan struct{}
	started    bool
	closed     bool
	dropped    func(topic string, data []byte, reason error)
}

func New() *PubSub {
	return &PubSub{
		exact:     make(map[string]*subscription),
		wildcards: make([]*subscription, 0),
		queue:     make(chan *Event, DefaultQueueSize),
	}
}

// Use 注册传输层，必须在 Start 之前调用
func (ps *PubSub) Use(t Transport) {
	ps.transports = append(ps.transports, t)
}

// SetQueue 设置远程消息队列容量，必须在 Start 之前调用。
// 详见 receive 上的说明：这个队列是用来把订阅回调从传输层的网络读协程上摘开的。
func (ps *PubSub) SetQueue(size int) {
	if size <= 0 {
		size = 1
	}
	ps.queue = make(chan *Event, size)
}

// OnDropped 远程消息未能投递时的回调，用于告警。本包不引入日志依赖，交由调用方处理。
// reason 区分丢弃原因（ErrQueueFull 或订阅回调 panic），必须在 Start 之前设置。
func (ps *PubSub) OnDropped(f func(topic string, data []byte, reason error)) {
	ps.dropped = f
}

func (ps *PubSub) Start() error {
	ps.mutex.Lock()
	if ps.started {
		ps.mutex.Unlock()
		return nil //重复 Start 会起出多个投递协程，破坏按序投递
	}
	ps.started = true
	if len(ps.transports) > 0 {
		ps.done = make(chan struct{})
		go ps.dispatch() //必须先起消费协程，再启动传输层
	}
	ps.mutex.Unlock()

	for _, t := range ps.transports {
		if err := t.Start(ps.receive); err != nil {
			return err
		}
	}
	return nil
}

func (ps *PubSub) Close() error {
	ps.mutex.Lock()
	if !ps.closed {
		ps.closed = true
		if ps.done != nil {
			close(ps.done)
		}
	}
	ps.mutex.Unlock()

	var last error
	for _, t := range ps.transports {
		if err := t.Close(); err != nil {
			last = err
		}
	}
	return last
}

// dispatch 远程消息的投递协程，单消费者保证事件按到达顺序处理
func (ps *PubSub) dispatch() {
	for {
		select {
		case <-ps.done:
			return
		case event := <-ps.queue:
			ps.deliverSafe(event)
		}
	}
}

// deliverSafe 订阅回调 panic 不能带走投递协程，否则后续事件永久停摆
func (ps *PubSub) deliverSafe(event *Event) {
	defer func() {
		if e := recover(); e != nil && ps.dropped != nil {
			ps.dropped(event.Topic, event.data, fmt.Errorf("pubsub: handler panic: %v", e))
		}
	}()
	ps.deliverLocal(event.Topic, event)
}

func (ps *PubSub) Subscribe(topic string, handler Handler) {
	ps.mutex.Lock()
	sub := ps.getOrCreate(topic)
	sub.Handlers = append(sub.Handlers, handler)
	ps.mutex.Unlock()

	if len(ps.transports) > 0 {
		for _, t := range ps.transports {
			t.Subscribe([]string{topic})
		}
	}
}

func (ps *PubSub) Unsubscribe(topic string) {
	ps.mutex.Lock()
	defer ps.mutex.Unlock()

	if isWildcard(topic) {
		for i, sub := range ps.wildcards {
			if sub.Topic == topic {
				newSubs := make([]*subscription, 0, len(ps.wildcards)-1)
				newSubs = append(newSubs, ps.wildcards[:i]...)
				newSubs = append(newSubs, ps.wildcards[i+1:]...)
				ps.wildcards = newSubs
				break
			}
		}
	} else if _, ok := ps.exact[topic]; ok {
		newExact := make(map[string]*subscription, len(ps.exact)-1)
		for k, v := range ps.exact {
			if k != topic {
				newExact[k] = v
			}
		}
		ps.exact = newExact
	}

	if len(ps.transports) > 0 {
		for _, t := range ps.transports {
			t.Unsubscribe([]string{topic})
		}
	}
}

// Publish 发布消息：先本地分发，再通过传输层远程分发
func (ps *PubSub) Publish(topic string, payload any) {
	event := newLocalEvent(topic, payload)
	ps.deliverLocal(topic, event)

	if len(ps.transports) > 0 {
		data, err := json.Marshal(payload)
		if err != nil {
			return
		}
		for _, t := range ps.transports {
			t.Publish(topic, data)
		}
	}
}

// receive 传输层回调，将远程消息分发给本地订阅者。
//
// 只入队，绝不在这里直接调订阅回调：所有传输层都是从自己的网络读协程调本函数的
// （cosnet 是 socket 的 readMsg 循环，redis 是 listen 协程），在订阅回调里做耗时
// 操作会把那条读协程卡住——cosnet 会因为读不到心跳被误判掉线、后续消息全堵在 TCP
// 缓冲里，redis 则会因为消费不及让 go-redis 的缓冲塞满、超时后直接丢消息。
// 队列满时宁可丢当前这条并告警，也不能反压回读协程。
func (ps *PubSub) receive(topic string, data []byte) {
	event := newRemoteEvent(topic, data)
	select {
	case ps.queue <- event:
	default:
		if ps.dropped != nil {
			ps.dropped(topic, data, ErrQueueFull)
		}
	}
}

func (ps *PubSub) deliverLocal(topic string, event *Event) {
	exact := ps.exact
	wildcards := ps.wildcards

	if sub, ok := exact[topic]; ok {
		for _, handler := range sub.Handlers {
			handler(event)
		}
	}
	for _, sub := range wildcards {
		if sub.Pattern.MatchString(topic) {
			for _, handler := range sub.Handlers {
				handler(event)
			}
		}
	}
}

func (ps *PubSub) getOrCreate(topic string) *subscription {
	if isWildcard(topic) {
		for _, sub := range ps.wildcards {
			if sub.Topic == topic {
				return sub
			}
		}
		sub := &subscription{Topic: topic, Pattern: compileWildcard(topic)}
		newSubs := make([]*subscription, len(ps.wildcards)+1)
		copy(newSubs, ps.wildcards)
		newSubs[len(ps.wildcards)] = sub
		ps.wildcards = newSubs
		return sub
	}
	if sub, ok := ps.exact[topic]; ok {
		return sub
	}
	sub := &subscription{Topic: topic}
	newExact := make(map[string]*subscription, len(ps.exact)+1)
	for k, v := range ps.exact {
		newExact[k] = v
	}
	newExact[topic] = sub
	ps.exact = newExact
	return sub
}

func (ps *PubSub) GetSubscriptions() []string {
	exact := ps.exact
	wildcards := ps.wildcards
	topics := make([]string, 0, len(exact)+len(wildcards))
	for topic := range exact {
		topics = append(topics, topic)
	}
	for _, sub := range wildcards {
		topics = append(topics, sub.Topic)
	}
	return topics
}

func (ps *PubSub) GetSubscriberCount(topic string) int {
	exact := ps.exact
	wildcards := ps.wildcards
	count := 0
	if _, ok := exact[topic]; ok {
		count++
	}
	for _, sub := range wildcards {
		if sub.Topic == topic {
			count++
			break
		}
	}
	return count
}
