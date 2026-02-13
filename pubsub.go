package pubsub

import (
	"fmt"
	"sync"
	"time"

	"github.com/hwcer/cosnet"
	"github.com/hwcer/cosnet/listener"
)

// Subscription 表示一个订阅
type Subscription struct {
	Topic    string
	Queue    string
	Handlers []func(topic string, msg any) // 支持多个handler
}

// PubSub 订阅发布系统
type PubSub struct {
	*cosnet.NetHub
	subscriptions sync.Map // 订阅列表，按主题分组，只存储本地订阅
	maxTTL        int      //消息最大传播跳数
}

// New 创建一个新的PubSub实例
func New(heartbeat int32) *PubSub {
	hub := cosnet.New(heartbeat)
	return NewWithNetHub(hub)
}

// NewWithNetHub 使用指定的cosnet实例创建PubSub
func NewWithNetHub(hub *cosnet.NetHub) *PubSub {
	pb := &PubSub{
		NetHub: hub,
		maxTTL: 10, // 默认最大传播跳数为10
	}
	handle := &handler{pb: pb}
	_ = pb.Register(handle, BasePath, "%m")

	// 注册连接断开事件，自动取消所有订阅
	pb.On(cosnet.EventTypeDisconnect, func(socket *cosnet.Socket, _ any) {
		pb.UnsubscribeAll(socket)
	})

	// 定期执行Gossip协议，交换服务器列表
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			pb.gossipServerList()
		}
	}()

	// 定期尝试连接已知的服务器
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			pb.connectToKnownServers()
		}
	}()

	return pb
}

// Connect 连接到其他服务器节点，并交换服务器信息
func (ps *PubSub) Connect(address string) (*cosnet.Socket, error) {
	// 检查是否已经存在到该服务器地址的连接
	alreadyConnected := false
	ps.Range(func(socket *cosnet.Socket) bool {
		if data := socket.Data(); data != nil {
			if serverAddress, ok := data.Get("server_address").(string); ok {
				if serverAddress == address {
					alreadyConnected = true
					return false
				}
			}
		}
		return true
	})

	// 如果已经连接，返回错误
	if alreadyConnected {
		return nil, fmt.Errorf("already connected to server %s", address)
	}

	socket, err := ps.NetHub.Connect(address)
	if err != nil {
		return nil, err
	}

	// 发送握手信息，交换服务器信息
	ps.sendHandshake(socket)

	return socket, nil
}

// GetServerInfo 获取服务器信息
func (ps *PubSub) GetServerInfo() ServerInfo {
	// 使用NetHub.Address()获取服务器地址并判断是否启动了服务器
	serverAddress := ps.Address()
	isServer := serverAddress != ""

	return ServerInfo{
		Address:      serverAddress,
		IsServer:     isServer,
		OtherServers: ps.getKnownServersList(),
	}
}

// GetKnownServers 获取已知的服务器列表
func (ps *PubSub) GetKnownServers() []string {
	return ps.getKnownServersList()
}

// IsServer 判断本地是否是服务器
func (ps *PubSub) IsServer() bool {
	return ps.Address() != ""
}

// Subscribe 订阅主题
func (ps *PubSub) Subscribe(topic string, handler func(topic string, msg interface{})) {
	// 使用IsServer方法判断是否启动了服务器
	isServer := ps.IsServer()

	// 所有节点（包括客户端）都创建本地订阅
	// 使用sync.Map的LoadOrStore方法确保主题存在
	if existingSub, ok := ps.subscriptions.Load(topic); ok {
		// 主题已存在，添加handler
		sub := existingSub.(Subscription)
		sub.Handlers = append(sub.Handlers, handler)
		ps.subscriptions.Store(topic, sub)
	} else {
		// 主题不存在，创建新的订阅
		ps.subscriptions.Store(topic, Subscription{
			Topic:    topic,
			Handlers: []func(topic string, msg interface{}){handler},
		})
	}

	// 只有客户端节点才需要向服务器发送订阅请求
	// 服务器节点不需要向其他节点发送订阅请求，因为作为服务器一定能收到其他节点发布的信息
	if !isServer {
		ps.Range(func(socket *cosnet.Socket) bool {
			socket.Send(0, 0, PathSubscribe, map[string]interface{}{
				"topic": topic,
			})
			return true
		})
	}
}

// QueueSubscribe 队列订阅，同一队列组的订阅者只有一个会收到消息
func (ps *PubSub) QueueSubscribe(topic string, queue string, handler func(topic string, msg interface{})) {
	// 使用IsServer方法判断是否启动了服务器
	isServer := ps.IsServer()

	// 所有节点（包括客户端）都创建本地订阅
	// 使用sync.Map的LoadOrStore方法确保主题存在
	if existingSub, ok := ps.subscriptions.Load(topic); ok {
		// 主题已存在，添加handler
		sub := existingSub.(Subscription)
		sub.Handlers = append(sub.Handlers, handler)
		sub.Queue = queue // 更新队列名称
		ps.subscriptions.Store(topic, sub)
	} else {
		// 主题不存在，创建新的订阅
		ps.subscriptions.Store(topic, Subscription{
			Topic:    topic,
			Queue:    queue,
			Handlers: []func(topic string, msg interface{}){handler},
		})
	}

	// 只有客户端节点才需要向服务器发送队列订阅请求
	// 服务器节点不需要向其他节点发送订阅请求，因为作为服务器一定能收到其他节点发布的信息
	if !isServer {
		ps.Range(func(socket *cosnet.Socket) bool {
			socket.Send(0, 0, PathQueueSubscribe, map[string]interface{}{
				"topic": topic,
				"queue": queue,
			})
			return true
		})
	}
}

// Unsubscribe 取消订阅主题
func (ps *PubSub) Unsubscribe(topic string) {
	// 使用IsServer方法判断是否启动了服务器
	isServer := ps.IsServer()

	// 所有节点都处理本地订阅
	// 使用sync.Map的Delete方法删除订阅
	ps.subscriptions.Delete(topic)

	// 只有客户端节点才需要向服务器发送取消订阅请求
	// 服务器节点不需要向其他节点发送取消订阅请求
	if !isServer {
		ps.Range(func(socket *cosnet.Socket) bool {
			socket.Send(0, 0, PathUnsubscribe, map[string]interface{}{
				"topics": []string{topic},
			})
			return true
		})
	}
}

// UnsubscribeAll 取消所有订阅
func (ps *PubSub) UnsubscribeAll(socket listener.Socket) {
	// 远程订阅存储在socket.Data中，连接断开时会自动清理
	// 这里只需要清理本地订阅
}

// Publish 发布消息到主题
// Publish 发布消息到指定主题
func (ps *PubSub) Publish(topic string, msg interface{}) {
	// 使用IsServer方法判断是否启动了服务器
	isServer := ps.IsServer()

	// 处理本地和远程订阅（所有节点都处理本地订阅）
	ps.processSubscriptions(topic, msg)

	// 只有服务器节点才分发消息到网络
	if isServer {
		// 分发消息到所有连接的服务器节点
		ps.Range(func(socket *cosnet.Socket) bool {
			// 检查对方是否是服务器节点
			if data := socket.Data(); data != nil {
				if isServerNode, ok := data.Get("is_server").(bool); ok && isServerNode {
					// 发送消息到服务器节点
					socket.Send(0, 0, PathPublish, map[string]interface{}{
						"topic":   topic,
						"message": msg,
						"ttl":     ps.maxTTL - 1,
					})
				}
			}
			return true
		})
	}
}
