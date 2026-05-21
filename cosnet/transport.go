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
	"github.com/hwcer/cosnet/listener"
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
}

func Listen(address string) *ServerTransport {
	return &ServerTransport{address: address, sockets: cosnet.New()}
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

// onRemotePublish 处理客户端发来的发布请求
func (t *ServerTransport) onRemotePublish(topic string, data []byte, source *cosnet.Socket) {
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
		if socket.Type() == listener.SocketTypeServer {
			t.syncSubscriptions(socket)
		}
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
