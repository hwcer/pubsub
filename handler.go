package pubsub

import (
	"errors"

	"github.com/hwcer/cosnet"
)

// handler 处理pubsub相关的网络请求
type handler struct {
	pb *PubSub
}

// Handshake 处理握手请求，交换服务器信息
func (h *handler) Handshake(c *cosnet.Context) interface{} {
	var data HandshakeData
	if err := c.Bind(&data); err != nil {
		return err
	}

	// 处理接收到的服务器信息
	h.pb.handleHandshake(c.Socket, data)

	// 发送自己的服务器信息
	return h.pb.GetServerInfo()
}

// Subscribe 处理订阅请求
func (h *handler) Subscribe(c *cosnet.Context) interface{} {
	var data struct {
		Topic string `json:"topic"`
	}
	if err := c.Bind(&data); err != nil {
		return err
	}

	// 只有服务器节点才存储订阅信息
	// 使用IsServer方法判断是否启动了服务器
	isServer := h.pb.IsServer()

	if isServer {
		// 将远程订阅存储在socket.Data中
		socketData := c.Socket.Data()
		if socketData == nil {
			return errors.New("socket data not initialized")
		}

		// 获取连接信息
		var connInfo ConnectionInfo
		if info, ok := socketData.Get("connection_info").(ConnectionInfo); ok {
			connInfo = info
		} else {
			// 初始化连接信息
			connInfo = ConnectionInfo{
				Subscriptions:      []string{},
				QueueSubscriptions: make(map[string]string),
			}
		}

		// 检查是否已经订阅
		for _, sub := range connInfo.Subscriptions {
			if sub == data.Topic {
				return errors.New("already subscribed")
			}
		}

		// 添加订阅
		connInfo.Subscriptions = append(connInfo.Subscriptions, data.Topic)
		socketData.Set("connection_info", connInfo)
	}

	return map[string]interface{}{
		"code":    "subscribed",
		"message": "订阅成功",
	}
}

// QueueSubscribe 处理队列订阅请求
func (h *handler) QueueSubscribe(c *cosnet.Context) interface{} {
	var data struct {
		Topic string `json:"topic"`
		Queue string `json:"queue"`
	}
	if err := c.Bind(&data); err != nil {
		return err
	}

	// 只有服务器节点才存储订阅信息
	// 使用IsServer方法判断是否启动了服务器
	isServer := h.pb.IsServer()

	if isServer {
		// 将远程队列订阅存储在socket.Data中
		socketData := c.Socket.Data()
		if socketData == nil {
			return errors.New("socket data not initialized")
		}

		// 获取连接信息
		var connInfo ConnectionInfo
		if info, ok := socketData.Get("connection_info").(ConnectionInfo); ok {
			connInfo = info
		} else {
			// 初始化连接信息
			connInfo = ConnectionInfo{
				Subscriptions:      []string{},
				QueueSubscriptions: make(map[string]string),
			}
		}

		// 检查是否已经订阅
		if _, exists := connInfo.QueueSubscriptions[data.Topic]; exists {
			return errors.New("already subscribed")
		}

		// 添加队列订阅
		connInfo.QueueSubscriptions[data.Topic] = data.Queue
		socketData.Set("connection_info", connInfo)
	}

	return map[string]interface{}{
		"code":    "subscribed",
		"message": "队列订阅成功",
	}
}

// Unsubscribe 处理取消订阅请求
func (h *handler) Unsubscribe(c *cosnet.Context) interface{} {
	var data struct {
		Topics []string `json:"topics"`
	}
	if err := c.Bind(&data); err != nil {
		return err
	}

	// 只有服务器节点才删除订阅信息
	// 使用IsServer方法判断是否启动了服务器
	isServer := h.pb.IsServer()

	if isServer {
		// 从socket.Data中删除远程订阅
		socketData := c.Socket.Data()
		if socketData != nil {
			// 获取连接信息
			var connInfo ConnectionInfo
			if info, ok := socketData.Get("connection_info").(ConnectionInfo); ok {
				connInfo = info

				// 处理普通订阅
				newSubscriptions := make([]string, 0, len(connInfo.Subscriptions))
				for _, sub := range connInfo.Subscriptions {
					found := false
					for _, topic := range data.Topics {
						if sub == topic {
							found = true
							break
						}
					}
					if !found {
						newSubscriptions = append(newSubscriptions, sub)
					}
				}
				connInfo.Subscriptions = newSubscriptions

				// 处理队列订阅
				for _, topic := range data.Topics {
					delete(connInfo.QueueSubscriptions, topic)
				}

				socketData.Set("connection_info", connInfo)
			}
		}
	}

	return map[string]interface{}{
		"code":    "unsubscribed",
		"message": "取消订阅成功",
	}
}

// Publish 处理发布请求
func (h *handler) Publish(c *cosnet.Context) interface{} {
	var data struct {
		Topic   string      `json:"topic"`
		Message interface{} `json:"message"`
		TTL     int         `json:"ttl"`
	}
	if err := c.Bind(&data); err != nil {
		return err
	}

	// 处理本地和远程订阅（所有节点都处理本地订阅）
	h.pb.processSubscriptions(data.Topic, data.Message)

	// 只有服务器节点才转发消息到网络
	// 使用IsServer方法判断是否启动了服务器
	isServer := h.pb.IsServer()

	if isServer {
		// 如果TTL大于0，继续分发消息
		if data.TTL > 0 {
			// 分发消息到所有连接的服务器节点
			h.pb.Range(func(socket *cosnet.Socket) bool {
				// 检查对方是否是服务器节点
				if socketData := socket.Data(); socketData != nil {
					if connInfo, ok := socketData.Get("connection_info").(ConnectionInfo); ok && connInfo.IsServer {
						// 发送消息到服务器节点
						socket.Send(0, 0, PathPublish, map[string]interface{}{
							"topic":   data.Topic,
							"message": data.Message,
							"ttl":     data.TTL - 1,
						})
					}
				}
				return true
			})
		}
	}

	return map[string]interface{}{
		"code":    "published",
		"message": "发布成功",
	}
}

// SubscribeList 处理获取订阅列表请求
func (h *handler) SubscribeList(c *cosnet.Context) interface{} {
	// 所有节点都可以获取订阅列表
	topics := h.pb.GetSubscriptions(c.Socket)
	return map[string]interface{}{
		"code":    "subscriptions",
		"message": "获取订阅列表成功",
		"data":    topics,
	}
}
