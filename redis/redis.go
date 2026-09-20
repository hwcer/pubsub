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
	"github.com/hwcer/logger"
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
// 🔴 统一单通道 PSubscribe(精确主题即字面 pattern),消除 SUBSCRIBE×PSUBSCRIBE
// 的跨型重复。注意:统一通道并不能消除"多个 pattern 命中同一 channel"的重复——
// redis 按"每消息 × 每命中 pattern"各投一份 pmessage(精确 a.b + 通配 a.> 仍两份),
// 消费侧由 patternExclusive 只放行字典序最小的命中副本。
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
	sub := t.client.PSubscribe(ctx)

	t.mu.Lock()
	t.sub = sub
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
		if err := sub.PSubscribe(ctx, patterns...); err != nil {
			cancel()
			return err
		}
	}

	go t.listen(ctx)
	go t.keepalive(ctx)
	return nil
}

func (t *Transport) Close() error {
	if t.cancel != nil {
		t.cancel()
	}
	t.mu.Lock()
	sub := t.sub
	t.mu.Unlock()
	if sub != nil {
		return sub.Close()
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
	sub := t.sub
	t.mu.Unlock()

	if sub != nil && len(topics) > 0 {
		patterns := make([]string, 0, len(topics))
		for _, topic := range topics {
			patterns = append(patterns, t.pattern(topic))
		}
		_ = sub.PSubscribe(context.Background(), patterns...)
	}
}

func (t *Transport) Unsubscribe(topics []string) {
	t.mu.Lock()
	for _, topic := range topics {
		delete(t.topics, topic)
	}
	sub := t.sub
	t.mu.Unlock()

	if sub != nil && len(topics) > 0 {
		patterns := make([]string, 0, len(topics))
		for _, topic := range topics {
			patterns = append(patterns, t.pattern(topic))
		}
		_ = sub.PUnsubscribe(context.Background(), patterns...)
	}
}

func (t *Transport) listen(ctx context.Context) {
	const minBackoff = 100 * time.Millisecond
	const maxBackoff = 30 * time.Second
	backoff := minBackoff
	for {
		//同步 Receive 循环:pmessage 回复由 go-redis 转为带 Pattern 的 *Message,
		//Channel() 的内部通道虽也能转发,但缓冲满 60s 会静默丢消息,且拿不到
		//Subscription 确认;同步循环直接消费,行为最透明
		msg, err := t.sub.Receive(ctx)
		if err != nil {
			//Close 先 cancel 再关 sub:ctx 已取消属正常退出
			if ctx.Err() != nil {
				return
			}
			//🔴 任意非取消错误(网络抖动/redis 重启)都不得让监听协程退出——
			//旧实现此处直接 return,传输层从此无声失联且无任何日志。go-redis 的
			//PubSub 在连接被标记为坏后会在下一次 Receive 内部重连并重放全部已
			//注册 pattern,这里退避重试即可自愈(与 Channel() 模式的 errCount 重试同构)
			logger.Error("pubsub redis transport receive error, retry in %v: %v", backoff, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < maxBackoff {
				backoff *= 2
			}
			continue
		}
		backoff = minBackoff
		switch m := msg.(type) {
		case *redis.Message:
			if m.Pattern != "" && !t.patternExclusive(m.Pattern, m.Channel) {
				continue //同一消息的重复副本(其他命中 pattern 投来的),丢弃
			}
			t.dispatch(m.Channel, m.Payload)
		case *redis.Subscription, *redis.Pong:
			continue //订阅确认/心跳
		default:
			continue
		}
	}
}

// keepalive 空闲周期 PING:半开连接(TCP 对端静默死亡)上 Receive 会永远阻塞。
// PING 写失败会把连接标记为坏并触发 go-redis 内部重连,从而解除 Receive 的阻塞读。
// PING 的 +PONG 回复由 listen 消费(case *redis.Pong),与 Channel() 模式的
// 健康检查同机制(ReceiveTimeout 注释明确允许并发 subscribe/ping)
func (t *Transport) keepalive(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := t.sub.Ping(ctx); err != nil && ctx.Err() == nil {
				logger.Error("pubsub redis transport ping error: %v", err)
			}
		}
	}
}

// patternExclusive 判定 pattern 是否"当前已订阅 pattern 中匹配 channel 的字典序
// 最小者"。redis 对同一消息按命中的每个 pattern 各投一份 pmessage(精确 a.b 与
// 通配 a.> 同订时仍两份),每份副本都足以驱动完整的本地投递(deliverLocal 自带
// 精确/通配分发),因此只放行字典序最小的一份、丢弃其余副本,去重不损失信息。
// 集合里没有任何 pattern 匹配时放行:宁投递不丢消息(仅当主题违反"不含 ?[] 元字符"
// 约定时可能发生,deliverLocal 的精确过滤仍兜底)。
func (t *Transport) patternExclusive(pattern, channel string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	min, matched := "", false
	for topic := range t.topics {
		p := t.pattern(topic)
		if !globMatch(p, channel) {
			continue
		}
		if !matched || p < min {
			min, matched = p, true
		}
	}
	if !matched {
		return true
	}
	return pattern == min
}

// globMatch 仅按 '*' 通配的 glob 匹配(pattern() 只产出 '*' 形态的通配,
// 主题按约定不含 redis glob 的 ? [ ] 元字符,其余字符按字面量比较)
func globMatch(pattern, s string) bool {
	pi, si, star, mark := 0, 0, -1, 0
	for si < len(s) {
		switch {
		case pi < len(pattern) && pattern[pi] == '*':
			star, mark, pi = pi, si, pi+1
		case pi < len(pattern) && pattern[pi] == s[si]:
			pi, si = pi+1, si+1
		case star >= 0:
			mark++
			si, pi = mark, star+1
		default:
			return false
		}
	}
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern)
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
