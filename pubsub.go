package pubsub

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sync"

	"github.com/hwcer/cosgo/session"
	"github.com/hwcer/cosgo/values"
	"github.com/hwcer/cosnet"
	"github.com/hwcer/cosnet/listener"
	"github.com/hwcer/cosnet/message"
)

// Mode 工作模式类型
type Mode int

const (
	ModeNone   Mode = iota // 未确定模式（单机内部调用）
	ModeClient             // 客户端模式
	ModeServer             // 服务器模式
)

// LocalSubscription 本地订阅
type LocalSubscription struct {
	Topic    string
	Handlers []SubscribeHandle // 支持多个handler
}

// PubSub 订阅发布系统
type PubSub struct {
	mode          Mode         // 工作模式
	mutex         sync.RWMutex // 保护 subscriptions 的读写锁
	sockets       *cosnet.Sockets
	subscriptions map[string]*LocalSubscription // 订阅列表，按主题分组
	regexCache    map[string]*regexp.Regexp     // 正则表达式缓存
}

// New 创建一个新的PubSub实例（通用）
func New() *PubSub {
	ps := &PubSub{
		sockets:       cosnet.New(),
		mode:          ModeNone,
		subscriptions: make(map[string]*LocalSubscription),
		regexCache:    make(map[string]*regexp.Regexp),
	}
	handler := ps.sockets.Handler()
	handler.SetSerialize(ps.serialize)
	// 注册连接建立事件，自动认证和同步订阅
	ps.sockets.On(cosnet.EventTypeConnected, func(socket *cosnet.Socket, _ any) {
		ps.onConnected(socket)
	})

	// 注册心跳事件
	ps.sockets.On(cosnet.EventTypeHeartbeat, func(socket *cosnet.Socket, msg any) {
		ps.onHeartbeat(socket, msg)
	})

	return ps
}

// NewServer 快速创建并启动服务器
func NewServer(address string) (*PubSub, error) {
	ps := New()
	err := ps.Listen(address)
	if err != nil {
		return nil, err
	}
	return ps, nil
}

// NewClient 快速创建客户端并连接到服务器
func NewClient(address string) (*PubSub, error) {
	ps := New()
	err := ps.Connect(address)
	if err != nil {
		return nil, err
	}
	return ps, nil
}
func (ps *PubSub) Start() error {
	if ps.mode == ModeNone {
		return nil
	}
	return ps.sockets.Start()
}

// Listen 启动服务器监听
func (ps *PubSub) Listen(address string) error {
	if ps.mode != ModeNone {
		return ErrAlreadyInitialized
	}
	ps.mode = ModeServer

	// 注册服务器handler
	handle := &serverHandler{pb: ps}
	_ = ps.sockets.Register(handle, BasePath, "%m")

	_, err := ps.sockets.Listen(address)
	return err
}

// Connect 连接到服务器（客户端使用）
func (ps *PubSub) Connect(address string) error {
	if ps.mode != ModeNone && ps.mode != ModeClient {
		return ErrAlreadyInitialized
	}
	ps.mode = ModeClient

	// 注册客户端handler
	handle := &clientHandler{pb: ps}
	_ = ps.sockets.Register(handle, BasePath, "%m")

	_, err := ps.sockets.Connect(address)
	if err != nil {
		return err
	}

	return nil
}

// Is 判断当前是否是指定的工作模式
func (ps *PubSub) Is(mode Mode) bool {
	return ps.mode == mode
}

// onConnected 连接建立时的回调
// 如果是客户端，将本地订阅批量同步到远程
func (ps *PubSub) onConnected(socket *cosnet.Socket) {
	// 自动认证
	v := session.NewData(fmt.Sprintf("%d", socket.Id()), nil)
	socket.Authentication(v)

	// 如果是客户端，批量同步本地订阅到远程
	if ps.Is(ModeClient) {
		ps.syncSubscriptionsToRemote(socket)
	}
}

// onHeartbeat 心跳事件处理
// 客户端：发送心跳信息给服务器
// 服务器：响应客户端的心跳
func (ps *PubSub) onHeartbeat(socket *cosnet.Socket, _ any) {
	if socket.Type() == listener.SocketTypeClient {
		flag := message.FlagHeartbeat + message.FlagACK
		_ = socket.Send(flag, 0, PathHeartbeat, nil)
	}
}

// syncSubscriptionsToRemote 将本地订阅同步到远程服务器（批量）
func (ps *PubSub) syncSubscriptionsToRemote(socket *cosnet.Socket) {
	ps.mutex.RLock()
	defer ps.mutex.RUnlock()

	if len(ps.subscriptions) == 0 {
		return
	}

	// 收集所有订阅主题
	topics := make([]string, 0, len(ps.subscriptions))
	for topic := range ps.subscriptions {
		topics = append(topics, topic)
	}

	// 批量发送订阅
	if len(topics) > 0 {
		flag := message.FlagACK
		_ = socket.Send(flag, 0, PathBatchSubscribe, BatchSubscription{Topics: topics})
	}
}

// getOrCreateSubscription 获取或创建订阅（调用层需要加锁）
func (ps *PubSub) getOrCreateSubscription(topic string) *LocalSubscription {
	if sub, ok := ps.subscriptions[topic]; ok {
		return sub
	}

	sub := &LocalSubscription{Topic: topic}
	ps.subscriptions[topic] = sub
	return sub
}

// Subscribe 订阅主题
func (ps *PubSub) Subscribe(topic string, handler SubscribeHandle) {
	// 获取或创建订阅，添加handler（在锁保护下完成）
	ps.mutex.Lock()
	sub := ps.getOrCreateSubscription(topic)
	sub.Handlers = append(sub.Handlers, handler)
	ps.mutex.Unlock()

	// 如果是客户端，向服务器发送订阅请求
	ps.sendToRemote(PathSubscribe, Subscription{Topic: topic})
}

// Unsubscribe 取消订阅主题
func (ps *PubSub) Unsubscribe(topic string) {
	ps.mutex.Lock()
	delete(ps.subscriptions, topic)
	ps.mutex.Unlock()

	// 如果是客户端，向服务器发送取消订阅请求
	ps.sendToRemote(PathUnsubscribe, Unsubscription{Topics: []string{topic}})
}

// UnsubscribeAll 取消所有订阅
// 清空本地订阅，如果是客户端则通知远程服务器
func (ps *PubSub) UnsubscribeAll() {
	// 获取所有本地订阅主题
	ps.mutex.RLock()
	topics := make([]string, 0, len(ps.subscriptions))
	for topic := range ps.subscriptions {
		topics = append(topics, topic)
	}
	ps.mutex.RUnlock()

	// 清空本地订阅
	ps.mutex.Lock()
	ps.subscriptions = make(map[string]*LocalSubscription)
	ps.mutex.Unlock()

	// 如果是客户端，通知远程服务器取消所有订阅
	if len(topics) > 0 {
		ps.sendToRemote(PathUnsubscribe, Unsubscription{Topics: topics})
	}
}

// Publish 发布消息到主题
func (ps *PubSub) Publish(topic string, msg any) {
	i := &Request{
		Topic:   topic,
		Payload: msg,
	}
	// 处理本地订阅和远程广播（无来源）
	ps.processSubscriptions(i, nil)

	// 如果是客户端，向服务器发送发布请求
	ps.sendToRemote(PathPublish, i)
}

// sendToRemote 向远程服务器发送消息（仅客户端模式）
func (ps *PubSub) sendToRemote(path string, data any) {
	if !ps.Is(ModeClient) {
		return
	}
	ps.sockets.Range(func(socket *cosnet.Socket) bool {
		flag := message.FlagACK
		_ = socket.Send(flag, 0, path, data)
		return true
	})
}

func (ps *PubSub) serialize(c *cosnet.Context, reply any) ([]byte, error) {
	var r any
	if err, ok := reply.(error); ok {
		r = values.Error(err)
	} else {
		r = values.Parse(reply)
	}
	return json.Marshal(r)
}

// compileWildcardPattern 编译通配符模式为正则表达式（带缓存）
func (ps *PubSub) compileWildcardPattern(pattern string) *regexp.Regexp {
	ps.mutex.RLock()
	if re, ok := ps.regexCache[pattern]; ok {
		ps.mutex.RUnlock()
		return re
	}
	ps.mutex.RUnlock()

	// 编译正则表达式
	rePattern := regexp.QuoteMeta(pattern)
	rePattern = regexp.MustCompile(`\*`).ReplaceAllString(rePattern, `[^.]+`)
	rePattern = regexp.MustCompile(`\>`).ReplaceAllString(rePattern, `.+`)
	rePattern = `^` + rePattern + `$`
	re := regexp.MustCompile(rePattern)

	// 缓存结果
	ps.mutex.Lock()
	ps.regexCache[pattern] = re
	ps.mutex.Unlock()

	return re
}
