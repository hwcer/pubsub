package redis

import (
	"context"
	"encoding/json"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/hwcer/pubsub"
)

var _ pubsub.Transport = (*Transport)(nil)

// envelope 消息信封，用于携带来源标识以过滤自身消息
type envelope struct {
	Origin string          `json:"o"`
	Data   json.RawMessage `json:"d"`
}

// Transport 基于 Redis Pub/Sub 的传输层，使用 per-topic channel
type Transport struct {
	id       string // 唯一实例标识，用于过滤自身消息
	client   *redis.Client
	prefix   string
	sub      *redis.PubSub
	receiver func(string, []byte)
	cancel   context.CancelFunc
	mu       sync.Mutex
	topics   map[string]bool
}

// New 创建 Redis 传输层，prefix 用于 channel 命名隔离（如 "myapp"）
func New(client *redis.Client, prefix string) *Transport {
	return &Transport{
		id:     strconv.FormatInt(time.Now().UnixNano(), 36) + strconv.FormatInt(rand.Int63(), 36),
		client: client,
		prefix: prefix,
		topics: make(map[string]bool),
	}
}

func (t *Transport) channel(topic string) string {
	if t.prefix == "" {
		return topic
	}
	return t.prefix + ":" + topic
}

// pattern 把主题转为 redis 的 PSUBSCRIBE 模式。
//
// 🔴 统一单通道 PSubscribe(精确主题即字面 pattern)。redis 对同一消息同时命中
// SUBSCRIBE 与 PSUBSCRIBE 时会各投一次:旧实现精确走 SUBSCRIBE、通配走 PSUBSCRIBE,
// 同进程"精确 a.b + 通配 a.*"双订阅会收到重复投递;单通道后所有订阅同型,天然无重复。
// 通配语义:pubsub 的 > 归一为 redis glob 的 *(宽松匹配),多收到的消息会在
// pubsub.deliverLocal 里按精确正则再过滤一次,只是多一点流量,不会误投递。
// 注意主题名里不要出现 redis glob 的元字符 ? [ ]。
func (t *Transport) pattern(topic string) string {
	if strings.ContainsAny(topic, "*>") {
		topic = strings.ReplaceAll(topic, ">", "*")
	}
	return t.channel(topic)
}

func (t *Transport) Start(receiver func(string, []byte)) error {
	t.receiver = receiver
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel = cancel
	t.sub = t.client.PSubscribe(ctx)

	t.mu.Lock()
	topics := make([]string, 0, len(t.topics))
	for topic := range t.topics {
		topics = append(topics, topic)
	}
	t.mu.Unlock()

	if len(topics) > 0 {
		patterns := make([]string, 0, len(topics))
		for _, topic := range topics {
			patterns = append(patterns, t.pattern(topic))
		}
		if err := t.sub.PSubscribe(ctx, patterns...); err != nil {
			cancel()
			return err
		}
	}

	go t.listen(ctx)
	return nil
}

func (t *Transport) Close() error {
	if t.cancel != nil {
		t.cancel()
	}
	if t.sub != nil {
		return t.sub.Close()
	}
	return nil
}

func (t *Transport) Publish(topic string, data []byte) error {
	env := envelope{Origin: t.id, Data: data}
	b, err := json.Marshal(&env)
	if err != nil {
		return err
	}
	return t.client.Publish(context.Background(), t.channel(topic), b).Err()
}

func (t *Transport) Subscribe(topics []string) {
	t.mu.Lock()
	for _, topic := range topics {
		t.topics[topic] = true
	}
	t.mu.Unlock()

	if t.sub != nil && len(topics) > 0 {
		patterns := make([]string, 0, len(topics))
		for _, topic := range topics {
			patterns = append(patterns, t.pattern(topic))
		}
		_ = t.sub.PSubscribe(context.Background(), patterns...)
	}
}

func (t *Transport) Unsubscribe(topics []string) {
	t.mu.Lock()
	for _, topic := range topics {
		delete(t.topics, topic)
	}
	t.mu.Unlock()

	if t.sub != nil && len(topics) > 0 {
		patterns := make([]string, 0, len(topics))
		for _, topic := range topics {
			patterns = append(patterns, t.pattern(topic))
		}
		_ = t.sub.PUnsubscribe(context.Background(), patterns...)
	}
}

func (t *Transport) listen(ctx context.Context) {
	for {
		//同步 Receive 循环:pmessage 回复由 go-redis 转为带 Pattern 的 *Message,
		//Channel() 的内部通道虽也能转发,但缓冲满 60s 会静默丢消息,且拿不到
		//Subscription 确认;同步循环直接消费,行为最透明
		msg, err := t.sub.Receive(ctx)
		if err != nil {
			return //ctx 取消或连接关闭
		}
		var channel, payload string
		switch m := msg.(type) {
		case *redis.Message:
			channel, payload = m.Channel, m.Payload
		case *redis.Subscription, *redis.Pong:
			continue //订阅确认/心跳
		default:
			continue
		}
		t.dispatch(channel, payload)
	}
}

func (t *Transport) dispatch(channel, payload string) {
	var env envelope
	if err := json.Unmarshal([]byte(payload), &env); err != nil {
		return
	}
	if env.Origin == t.id {
		return
	}
	topic := channel
	if t.prefix != "" {
		prefixLen := len(t.prefix) + 1
		if len(topic) > prefixLen {
			topic = topic[prefixLen:]
		}
	}
	if t.receiver != nil {
		t.receiver(topic, env.Data)
	}
}
