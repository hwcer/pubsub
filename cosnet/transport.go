package cosnet

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/hwcer/cosgo/session"
	"github.com/hwcer/cosgo/values"
	"github.com/hwcer/cosnet"
	"github.com/hwcer/cosnet/message"
	"github.com/hwcer/pubsub"
)

var _ pubsub.Transport = (*ServerTransport)(nil)
var _ pubsub.Transport = (*ClientTransport)(nil)

// wildcardCache 用于服务端检查 socket 订阅是否匹配
var wildcardCache sync.Map

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

func serialize(_ *cosnet.Context, reply any) ([]byte, error) {
	var r any
	if err, ok := reply.(error); ok {
		r = values.Error(err)
	} else {
		r = values.Parse(reply)
	}
	return json.Marshal(r)
}

// ServerTransport 服务端传输层，监听端口并向已订阅的客户端广播消息
type ServerTransport struct {
	address  string
	sockets  *cosnet.Sockets
	receiver func(topic string, data []byte)
	readOnly bool
}

func Listen(address string) *ServerTransport {
	return &ServerTransport{address: address, sockets: cosnet.New()}
}

// Options 返回底层 cosnet 配置，必须在 Start 之前修改。
// cosnet.New() 是把包级 Options 值拷贝进实例，所以只能通过本方法调整本实例的配置。
func (t *ServerTransport) Options() *cosnet.Config {
	return &t.sockets.Options
}

// ReadOnly 只读模式：忽略客户端发来的 publish，仅单向下发。
// 服务端对每个连入的 socket 无条件 Authentication，若不开启只读，
// 任何能连到本端口的客户端都能把消息注入本进程的事件总线。
// 单向下发场景（服务端发布、客户端只订阅）应一律开启。
func (t *ServerTransport) ReadOnly(b bool) {
	t.readOnly = b
}

func (t *ServerTransport) Start(receiver func(string, []byte)) error {
	t.receiver = receiver
	handler := t.sockets.Handler()
	handler.SetSerialize(serialize)
	t.sockets.On(cosnet.EventTypeConnected, func(socket *cosnet.Socket, _ any) {
		data := socket.Data()
		if data == nil {
			data = session.NewData(fmt.Sprintf("%d", socket.Id()), nil)
		}
		socket.Authentication(data)
	})
	_ = t.sockets.Register(&serverHandler{transport: t}, basePath, "%m")
	if _, err := t.sockets.Listen(t.address); err != nil {
		return err
	}
	return t.sockets.Start()
}

func (t *ServerTransport) Close() error {
	return nil
}

func (t *ServerTransport) Publish(topic string, data []byte) error {
	t.sockets.Range(func(socket *cosnet.Socket) bool {
		if shouldSendToSocket(socket, topic) {
			socket.Send(0, 0, pathMessage, &publishReq{Topic: topic, Data: data})
		}
		return true
	})
	return nil
}

func (t *ServerTransport) Subscribe(_ []string)   {}
func (t *ServerTransport) Unsubscribe(_ []string) {}

// onRemotePublish 处理客户端发来的发布请求，只读模式下直接丢弃
func (t *ServerTransport) onRemotePublish(topic string, data []byte, source *cosnet.Socket) {
	if t.readOnly {
		return
	}
	if t.receiver != nil {
		t.receiver(topic, data)
	}
	t.sockets.Range(func(socket *cosnet.Socket) bool {
		if socket.Is(source) {
			return true
		}
		if shouldSendToSocket(socket, topic) {
			socket.Send(0, 0, pathMessage, &publishReq{Topic: topic, Data: data})
		}
		return true
	})
}

func shouldSendToSocket(socket *cosnet.Socket, topic string) bool {
	data := socket.Data()
	if data == nil {
		return false
	}
	subs, ok := data.Get(socketDataKeySubscriptions).([]string)
	if !ok {
		return false
	}
	for _, subTopic := range subs {
		if subTopic == topic {
			return true
		}
		if isWildcard(subTopic) && compileWildcard(subTopic).MatchString(topic) {
			return true
		}
	}
	return false
}

// ClientTransport 客户端传输层，连接到服务器并同步订阅
type ClientTransport struct {
	address  string
	sockets  *cosnet.Sockets
	receiver func(topic string, data []byte)
	topics   []string
	mu       sync.Mutex
}

func Connect(address string) *ClientTransport {
	return &ClientTransport{address: address, sockets: cosnet.New()}
}

// Options 返回底层 cosnet 配置，必须在 Start 之前修改。
// 典型用法：ClientReconnectMax = 0 表示无限重连。默认值 10 在约 3 分钟后彻底放弃重连，
// 之后既不会再连上也不会报错，表现为永久静默失联。
func (t *ClientTransport) Options() *cosnet.Config {
	return &t.sockets.Options
}

func (t *ClientTransport) Start(receiver func(string, []byte)) error {
	t.receiver = receiver
	handler := t.sockets.Handler()
	handler.SetSerialize(serialize)
	t.sockets.On(cosnet.EventTypeConnected, func(socket *cosnet.Socket, _ any) {
		data := socket.Data()
		if data == nil {
			data = session.NewData(fmt.Sprintf("%d", socket.Id()), nil)
		}
		socket.Authentication(data)
		//本实例只有主动发起的连接，无条件同步订阅。
		//不能用 Type() 做判断：首次连接时 address 尚未赋值（Connect 在 Create 之后才设），
		//重连时已有值，两次返回的类型不同，会导致重连后订阅丢失、连上却收不到任何消息。
		t.syncSubscriptions(socket)
	})
	//空闲连接会被 cosnet 的心跳判死：KeepAlive 只在收包时触发，而守护协程每
	//Options.Heartbeat 秒累加一次，超过 SocketConnectTime 即断开。事件总线平时无流量，
	//必须主动发心跳——收到服务端回包时两侧的活跃时间才会一起刷新。
	t.sockets.On(cosnet.EventTypeHeartbeat, func(socket *cosnet.Socket, _ any) {
		_ = socket.Send(message.Flag(0), 0, pathPing, pingReq{})
	})
	_ = t.sockets.Register(&clientHandler{transport: t}, basePath, "%m")
	if _, err := t.sockets.Connect(t.address); err != nil {
		return err
	}
	return t.sockets.Start()
}

func (t *ClientTransport) Close() error {
	return nil
}

func (t *ClientTransport) Publish(topic string, data []byte) error {
	t.sockets.Range(func(socket *cosnet.Socket) bool {
		_ = socket.Send(message.Flag(0), 0, pathPublish, &publishReq{Topic: topic, Data: data})
		return true
	})
	return nil
}

func (t *ClientTransport) Subscribe(topics []string) {
	t.mu.Lock()
	t.topics = append(t.topics, topics...)
	t.mu.Unlock()

	t.sockets.Range(func(socket *cosnet.Socket) bool {
		for _, topic := range topics {
			_ = socket.Send(message.Flag(0), 0, pathSubscribe, subscriptionReq{Topic: topic})
		}
		return true
	})
}

func (t *ClientTransport) Unsubscribe(topics []string) {
	t.mu.Lock()
	remove := make(map[string]bool, len(topics))
	for _, topic := range topics {
		remove[topic] = true
	}
	filtered := make([]string, 0, len(t.topics))
	for _, topic := range t.topics {
		if !remove[topic] {
			filtered = append(filtered, topic)
		}
	}
	t.topics = filtered
	t.mu.Unlock()

	t.sockets.Range(func(socket *cosnet.Socket) bool {
		_ = socket.Send(message.Flag(0), 0, pathUnsubscribe, unsubscriptionReq{Topics: topics})
		return true
	})
}

func (t *ClientTransport) syncSubscriptions(socket *cosnet.Socket) {
	t.mu.Lock()
	topics := make([]string, len(t.topics))
	copy(topics, t.topics)
	t.mu.Unlock()

	if len(topics) > 0 {
		_ = socket.Send(message.Flag(0), 0, pathBatchSubscribe, batchSubscriptionReq{Topics: topics})
	}
}
