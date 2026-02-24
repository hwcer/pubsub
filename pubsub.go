package pubsub

import (
	"fmt"
	"sync"

	"github.com/hwcer/cosgo/session"
	"github.com/hwcer/cosnet"
)

// Mode 工作模式类型
type Mode int

const (
	ModeNone   Mode = iota // 未确定模式（初始状态）
	ModeClient             // 客户端模式
	ModeServer             // 服务器模式
)

// LocalSubscription 本地订阅
type LocalSubscription struct {
	Topic    string
	Handlers []func(topic string, msg any) // 支持多个handler
}

// PubSub 订阅发布系统
type PubSub struct {
	mode          Mode         // 工作模式
	subMu         sync.RWMutex // 保护 subscriptions 的读写锁
	sockets       *cosnet.Sockets
	subscriptions map[string]*LocalSubscription // 订阅列表，按主题分组
}

// New 创建一个新的PubSub实例（通用）
func New() *PubSub {
	ps := &PubSub{
		sockets:       cosnet.New(10),
		mode:          ModeNone,
		subscriptions: make(map[string]*LocalSubscription),
	}

	// 注册连接建立事件，自动认证和同步订阅
	ps.sockets.On(cosnet.EventTypeConnected, func(socket *cosnet.Socket, _ any) {
		ps.onConnected(socket)
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

// syncSubscriptionsToRemote 将本地订阅同步到远程服务器（批量）
func (ps *PubSub) syncSubscriptionsToRemote(socket *cosnet.Socket) {
	ps.subMu.RLock()
	defer ps.subMu.RUnlock()

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
		socket.Send(0, 0, PathBatchSubscribe, BatchSubscription{Topics: topics})
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
func (ps *PubSub) Subscribe(topic string, handler func(topic string, msg interface{})) {
	// 获取或创建订阅，添加handler（在锁保护下完成）
	ps.subMu.Lock()
	sub := ps.getOrCreateSubscription(topic)
	sub.Handlers = append(sub.Handlers, handler)
	ps.subMu.Unlock()

	// 如果是客户端，向服务器发送订阅请求
	ps.sendToRemote(PathSubscribe, Subscription{Topic: topic})
}

// Unsubscribe 取消订阅主题
func (ps *PubSub) Unsubscribe(topic string) {
	ps.subMu.Lock()
	delete(ps.subscriptions, topic)
	ps.subMu.Unlock()

	// 如果是客户端，向服务器发送取消订阅请求
	ps.sendToRemote(PathUnsubscribe, Unsubscription{Topics: []string{topic}})
}

// UnsubscribeAll 取消所有订阅
// 清空本地订阅，如果是客户端则通知远程服务器
func (ps *PubSub) UnsubscribeAll() {
	// 获取所有本地订阅主题
	ps.subMu.RLock()
	topics := make([]string, 0, len(ps.subscriptions))
	for topic := range ps.subscriptions {
		topics = append(topics, topic)
	}
	ps.subMu.RUnlock()

	// 清空本地订阅
	ps.subMu.Lock()
	ps.subscriptions = make(map[string]*LocalSubscription)
	ps.subMu.Unlock()

	// 如果是客户端，通知远程服务器取消所有订阅
	if len(topics) > 0 {
		ps.sendToRemote(PathUnsubscribe, Unsubscription{Topics: topics})
	}
}

// Publish 发布消息到主题
func (ps *PubSub) Publish(topic string, msg interface{}) {
	// 处理本地订阅
	ps.processSubscriptions(topic, msg)

	// 如果是客户端，向服务器发送发布请求
	ps.sendToRemote(PathPublish, Publication{
		Topic:   topic,
		Payload: msg,
	})
}

// sendToRemote 向远程服务器发送消息（仅客户端模式）
func (ps *PubSub) sendToRemote(path string, data interface{}) {
	if !ps.Is(ModeClient) || ps.sockets == nil {
		return
	}
	ps.sockets.Range(func(socket *cosnet.Socket) bool {
		socket.Send(0, 0, path, data)
		return true
	})
}
