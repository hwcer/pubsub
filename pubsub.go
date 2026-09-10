package pubsub

import (
	"encoding/json"
	"maps"
	"regexp"
	"sync"

	"github.com/hwcer/logger"
)

type subscription struct {
	Topic    string
	Pattern  *regexp.Regexp // 通配符预编译正则，精确匹配时为 nil
	Handlers []Handler
}

// DefaultQueueSize 远程消息队列的默认容量
const DefaultQueueSize = 256

// Logger 日志输出接口，方法集与 github.com/hwcer/logger 对齐，
// 可直接传入 logger.New()。默认输出到 logger 的默认实例。
type Logger interface {
	Error(format any, args ...any)
}

// defaultLogger 默认实现，转发到 hwcer/logger 的包级默认实例
type defaultLogger struct{}

func (defaultLogger) Error(format any, args ...any) {
	logger.Error(format, args...)
}

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
	startErr   error //首次Start失败的原因,重复Start时如实返回
	closed     bool
	logger     Logger
}

func New() *PubSub {
	return &PubSub{
		exact:     make(map[string]*subscription),
		wildcards: make([]*subscription, 0),
		queue:     make(chan *Event, DefaultQueueSize),
		logger:    defaultLogger{},
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

// SetLogger 覆盖默认日志实现，用于输出远程消息投递失败（队列满、订阅回调 panic）
// 等信息。传 nil 表示静默丢弃。必须在 Start 之前设置。
func (ps *PubSub) SetLogger(l Logger) {
	ps.logger = l
}

func (ps *PubSub) errorf(format string, args ...any) {
	if ps.logger != nil {
		ps.logger.Error(format, args...)
	}
}

func (ps *PubSub) Start() error {
	ps.mutex.Lock()
	if ps.started {
		err := ps.startErr
		ps.mutex.Unlock()
		//重复 Start 会起出多个投递协程，破坏按序投递;
		//上次失败过则如实返回错误,而不是静默假装成功
		return err
	}
	ps.started = true
	if len(ps.transports) > 0 {
		ps.done = make(chan struct{})
		go ps.dispatch() //必须先起消费协程，再启动传输层
	}
	ps.mutex.Unlock()

	//Use 可能晚于 Subscribe（订阅常在包 init 里注册，传输层要到 Start 前才装配好），
	//这些先注册的订阅走 Subscribe 时 transports 还是空的、没能转发出去，在这里补上。
	topics := ps.topics()
	for _, t := range ps.transports {
		if len(topics) > 0 {
			t.Subscribe(topics)
		}
		if err := t.Start(ps.receive); err != nil {
			ps.mutex.Lock()
			ps.startErr = err
			ps.mutex.Unlock()
			return err
		}
	}
	return nil
}

// topics 当前已注册的全部订阅主题（含通配）
func (ps *PubSub) topics() []string {
	ps.mutex.Lock()
	defer ps.mutex.Unlock()
	r := make([]string, 0, len(ps.exact)+len(ps.wildcards))
	for topic := range ps.exact {
		r = append(r, topic)
	}
	for _, sub := range ps.wildcards {
		r = append(r, sub.Topic)
	}
	return r
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
		if e := recover(); e != nil {
			ps.errorf("pubsub: handler panic, topic:%v, error:%v", event.Topic, e)
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
	//本地回调同样可能panic,不能带走发布方(常在网络读协程中调用),与远程路径口径一致
	ps.deliverSafe(event)

	if len(ps.transports) > 0 {
		data, err := json.Marshal(payload)
		if err != nil {
			ps.errorf("pubsub: payload marshal failed, topic:%v, error:%v", topic, err)
			return
		}
		for _, t := range ps.transports {
			if err := t.Publish(topic, data); err != nil {
				//远程分发失败(如断连)不应静默丢消息,至少留下日志
				ps.errorf("pubsub: transport publish failed, topic:%v, error:%v", topic, err)
			}
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
		ps.errorf("pubsub: queue is full, message dropped, topic:%v", topic)
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
	maps.Copy(newExact, ps.exact)
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
