package pubsub

import (
	"encoding/json"
	"regexp"
	"sync"
)

type subscription struct {
	Topic    string
	Pattern  *regexp.Regexp // 通配符预编译正则，精确匹配时为 nil
	Handlers []Handler
}

// PubSub 事件总线，支持本地发布/订阅和可插拔的远程传输层
// 使用 COW 模式优化读取性能
type PubSub struct {
	mutex      sync.Mutex
	transports []Transport
	exact      map[string]*subscription
	wildcards  []*subscription
}

func New() *PubSub {
	return &PubSub{
		exact:     make(map[string]*subscription),
		wildcards: make([]*subscription, 0),
	}
}

// Use 注册传输层，必须在 Start 之前调用
func (ps *PubSub) Use(t Transport) {
	ps.transports = append(ps.transports, t)
}

func (ps *PubSub) Start() error {
	for _, t := range ps.transports {
		if err := t.Start(ps.receive); err != nil {
			return err
		}
	}
	return nil
}

func (ps *PubSub) Close() error {
	var last error
	for _, t := range ps.transports {
		if err := t.Close(); err != nil {
			last = err
		}
	}
	return last
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

// receive 传输层回调，将远程消息分发给本地订阅者
func (ps *PubSub) receive(topic string, data []byte) {
	event := newRemoteEvent(topic, data)
	ps.deliverLocal(topic, event)
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
