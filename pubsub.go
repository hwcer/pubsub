package pubsub

import (
	"encoding/json"
	"fmt"
	"strings"
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
// 使用 COW (Copy-On-Write) 模式优化读取性能
// 注意：此实现假设在 64 位架构上运行（x86_64, ARM64）
// 在 32 位架构上可能存在并发安全问题
type PubSub struct {
	mode                  Mode       // 工作模式
	mutex                 sync.Mutex // 保护订阅列表的写锁（COW 模式）
	sockets               *cosnet.Sockets
	exactSubscriptions    map[string]*LocalSubscription // 精确匹配的订阅（COW 模式读取）
	wildcardSubscriptions []*LocalSubscription          // 通配符匹配的订阅（COW 模式读取）
}

// New 创建一个新的PubSub实例（通用）
func New() *PubSub {
	ps := &PubSub{
		sockets:               cosnet.New(),
		mode:                  ModeNone,
		exactSubscriptions:    make(map[string]*LocalSubscription),
		wildcardSubscriptions: make([]*LocalSubscription, 0),
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
	return err
}

// Is 检查当前工作模式
func (ps *PubSub) Is(mode Mode) bool {
	return ps.mode == mode
}

// onConnected 连接建立时的回调
func (ps *PubSub) onConnected(socket *cosnet.Socket) {
	// 使用 SOCKETID 进行认证
	data := socket.Data()
	if data == nil {
		data = session.NewData(fmt.Sprintf("%d", socket.Id()), nil)
	}
	socket.Authentication(data)

	// 设置 socket 类型到 session 数据
	if data == nil {
		return
	}

	// 根据 socket 类型执行不同逻辑
	if socket.Type() == listener.SocketTypeClient {
		// 服务器接受客户端连接
		// 可以在这里进行额外的认证或初始化
	} else if socket.Type() == listener.SocketTypeServer {
		// 客户端连接到服务器
		// 自动同步本地订阅到远程服务器
		ps.syncSubscriptionsToRemote(socket)
	}
}

// onHeartbeat 心跳事件处理
func (ps *PubSub) onHeartbeat(socket *cosnet.Socket, msg any) {
	if socket.Type() == listener.SocketTypeClient {
		// 服务器收到客户端心跳，直接返回 nil 表示正常响应
		// 可以在这里更新客户端活跃时间等
	} else if socket.Type() == listener.SocketTypeServer {
		// 客户端收到服务器心跳响应
		// 可以在这里处理心跳超时逻辑、重连等
	}
}

// syncSubscriptionsToRemote 将本地订阅同步到远程服务器（批量）
func (ps *PubSub) syncSubscriptionsToRemote(socket *cosnet.Socket) {
	// COW 模式读取：直接读取，无需加锁
	// 注意：此操作在 64 位架构上是安全的
	exactSubs := ps.exactSubscriptions
	wildcardSubs := ps.wildcardSubscriptions

	totalSubscriptions := len(exactSubs) + len(wildcardSubs)
	if totalSubscriptions == 0 {
		return
	}

	// 收集所有订阅主题
	topics := make([]string, 0, totalSubscriptions)
	for topic := range exactSubs {
		topics = append(topics, topic)
	}
	for _, sub := range wildcardSubs {
		topics = append(topics, sub.Topic)
	}

	// 批量发送订阅
	if len(topics) > 0 {
		flag := message.FlagACK
		_ = socket.Send(flag, 0, PathBatchSubscribe, BatchSubscription{Topics: topics})
	}
}

// getOrCreateSubscription 获取或创建订阅（调用层需要加锁）
func (ps *PubSub) getOrCreateSubscription(topic string) *LocalSubscription {
	// 检查是否是通配符订阅
	isWildcard := strings.Contains(topic, "*") || strings.Contains(topic, ">")

	if isWildcard {
		// 通配符订阅
		for _, sub := range ps.wildcardSubscriptions {
			if sub.Topic == topic {
				return sub
			}
		}
		sub := &LocalSubscription{Topic: topic}
		// 复制切片并添加新订阅
		newWildcardSubs := make([]*LocalSubscription, len(ps.wildcardSubscriptions)+1)
		copy(newWildcardSubs, ps.wildcardSubscriptions)
		newWildcardSubs[len(ps.wildcardSubscriptions)] = sub
		ps.wildcardSubscriptions = newWildcardSubs
		return sub
	} else {
		// 精确匹配订阅
		if sub, ok := ps.exactSubscriptions[topic]; ok {
			return sub
		}
		sub := &LocalSubscription{Topic: topic}
		// 复制 map 并添加新订阅
		newExactSubs := make(map[string]*LocalSubscription, len(ps.exactSubscriptions)+1)
		for k, v := range ps.exactSubscriptions {
			newExactSubs[k] = v
		}
		newExactSubs[topic] = sub
		ps.exactSubscriptions = newExactSubs
		return sub
	}
}

// Subscribe 订阅主题
func (ps *PubSub) Subscribe(topic string, handler SubscribeHandle) {
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
	defer ps.mutex.Unlock()

	// 检查是否是通配符订阅
	isWildcard := strings.Contains(topic, "*") || strings.Contains(topic, ">")

	if isWildcard {
		// 从通配符订阅列表中移除
		for i, sub := range ps.wildcardSubscriptions {
			if sub.Topic == topic {
				// 复制切片并移除元素
				newWildcardSubs := make([]*LocalSubscription, 0, len(ps.wildcardSubscriptions)-1)
				newWildcardSubs = append(newWildcardSubs, ps.wildcardSubscriptions[:i]...)
				newWildcardSubs = append(newWildcardSubs, ps.wildcardSubscriptions[i+1:]...)
				ps.wildcardSubscriptions = newWildcardSubs
				break
			}
		}
	} else {
		// 从精确匹配订阅中移除
		if _, ok := ps.exactSubscriptions[topic]; ok {
			// 复制 map 并移除元素
			newExactSubs := make(map[string]*LocalSubscription, len(ps.exactSubscriptions)-1)
			for k, v := range ps.exactSubscriptions {
				if k != topic {
					newExactSubs[k] = v
				}
			}
			ps.exactSubscriptions = newExactSubs
		}
	}

	// 如果是客户端，向服务器发送取消订阅请求
	ps.sendToRemote(PathUnsubscribe, Unsubscription{Topics: []string{topic}})
}

// UnsubscribeAll 取消所有订阅
// 清空本地订阅，如果是客户端则通知远程服务器
func (ps *PubSub) UnsubscribeAll() {
	ps.mutex.Lock()
	defer ps.mutex.Unlock()

	// 获取所有本地订阅主题
	topics := make([]string, 0, len(ps.exactSubscriptions)+len(ps.wildcardSubscriptions))
	for topic := range ps.exactSubscriptions {
		topics = append(topics, topic)
	}
	for _, sub := range ps.wildcardSubscriptions {
		topics = append(topics, sub.Topic)
	}

	// 清空本地订阅（创建新的空集合）
	ps.exactSubscriptions = make(map[string]*LocalSubscription)
	ps.wildcardSubscriptions = make([]*LocalSubscription, 0)

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
