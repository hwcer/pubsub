package redis

import (
	"context"
	"encoding/json"
	"math/rand"
	"strconv"
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

func (t *Transport) Start(receiver func(string, []byte)) error {
	t.receiver = receiver
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel = cancel
	t.sub = t.client.Subscribe(ctx)

	t.mu.Lock()
	channels := make([]string, 0, len(t.topics))
	for topic := range t.topics {
		channels = append(channels, t.channel(topic))
	}
	t.mu.Unlock()

	if len(channels) > 0 {
		if err := t.sub.Subscribe(ctx, channels...); err != nil {
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

	if t.sub != nil {
		channels := make([]string, len(topics))
		for i, topic := range topics {
			channels[i] = t.channel(topic)
		}
		t.sub.Subscribe(context.Background(), channels...)
	}
}

func (t *Transport) Unsubscribe(topics []string) {
	t.mu.Lock()
	for _, topic := range topics {
		delete(t.topics, topic)
	}
	t.mu.Unlock()

	if t.sub != nil {
		channels := make([]string, len(topics))
		for i, topic := range topics {
			channels[i] = t.channel(topic)
		}
		t.sub.Unsubscribe(context.Background(), channels...)
	}
}

func (t *Transport) listen(ctx context.Context) {
	ch := t.sub.Channel()
	prefixLen := 0
	if t.prefix != "" {
		prefixLen = len(t.prefix) + 1
	}
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			var env envelope
			if err := json.Unmarshal([]byte(msg.Payload), &env); err != nil {
				continue
			}
			if env.Origin == t.id {
				continue
			}
			topic := msg.Channel
			if prefixLen > 0 && len(topic) > prefixLen {
				topic = topic[prefixLen:]
			}
			if t.receiver != nil {
				t.receiver(topic, env.Data)
			}
		}
	}
}
