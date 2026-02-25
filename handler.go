package pubsub

import (
	"errors"

	"github.com/hwcer/cosnet"
)

// serverHandler 服务器端的请求处理器
type serverHandler struct {
	pb *PubSub
}

// getSubscriptions 获取订阅列表
func (h *serverHandler) getSubscriptions(c *cosnet.Context) []string {
	socketData := c.Socket.Data()
	if socketData == nil {
		return nil
	}
	if subs, ok := socketData.Get(SocketDataKeySubscriptions).([]string); ok {
		return subs
	}
	return nil
}

// setSubscriptions 设置订阅列表
func (h *serverHandler) setSubscriptions(c *cosnet.Context, subs []string) {
	if socketData := c.Socket.Data(); socketData != nil {
		socketData.Set(SocketDataKeySubscriptions, subs)
	}
}

// BatchSubscribe 处理客户端的批量订阅请求
func (h *serverHandler) BatchSubscribe(c *cosnet.Context) interface{} {
	var data BatchSubscription
	if err := c.Bind(&data); err != nil {
		return err
	}

	socketData := c.Socket.Data()
	if socketData == nil {
		return errors.New("socket data not initialized")
	}

	// 获取现有订阅
	existingSubs := h.getSubscriptions(c)
	subsMap := make(map[string]bool, len(existingSubs))
	for _, sub := range existingSubs {
		subsMap[sub] = true
	}

	// 批量添加订阅（去重）
	added := 0
	for _, topic := range data.Topics {
		if !subsMap[topic] {
			existingSubs = append(existingSubs, topic)
			subsMap[topic] = true
			added++
		}
	}

	h.setSubscriptions(c, existingSubs)

	return map[string]interface{}{
		"code":    "batch_subscribed",
		"message": "批量订阅成功",
		"count":   added,
	}
}

// Subscribe 处理客户端的订阅请求
func (h *serverHandler) Subscribe(c *cosnet.Context) interface{} {
	var data Subscription
	if err := c.Bind(&data); err != nil {
		return err
	}

	socketData := c.Socket.Data()
	if socketData == nil {
		return errors.New("socket data not initialized")
	}

	// 获取现有订阅
	subs := h.getSubscriptions(c)

	// 检查是否已经订阅
	for _, sub := range subs {
		if sub == data.Topic {
			return errors.New("already subscribed")
		}
	}

	subs = append(subs, data.Topic)
	h.setSubscriptions(c, subs)

	return map[string]interface{}{
		"code":    "subscribed",
		"message": "订阅成功",
	}
}

// Unsubscribe 处理客户端的取消订阅请求
func (h *serverHandler) Unsubscribe(c *cosnet.Context) interface{} {
	var data Unsubscription
	if err := c.Bind(&data); err != nil {
		return err
	}

	// 处理订阅
	subs := h.getSubscriptions(c)
	if subs != nil {
		newSubs := make([]string, 0, len(subs))
		for _, sub := range subs {
			found := false
			for _, topic := range data.Topics {
				if sub == topic {
					found = true
					break
				}
			}
			if !found {
				newSubs = append(newSubs, sub)
			}
		}
		h.setSubscriptions(c, newSubs)
	}

	return map[string]interface{}{
		"code":    "unsubscribed",
		"message": "取消订阅成功",
	}
}

// Publish 处理客户端的发布请求
func (h *serverHandler) Publish(c *cosnet.Context) interface{} {
	var data Publication
	if err := c.Bind(&data); err != nil {
		return err
	}

	// 处理本地订阅
	h.pb.processSubscriptions(data.Topic, data.Payload)

	// 广播给其他客户端（不包括来源）
	h.pb.sockets.Range(func(socket *cosnet.Socket) bool {
		// 跳过来源socket
		if socket == c.Socket {
			return true
		}

		if socketData := socket.Data(); socketData != nil {
			// 检查订阅
			if subs, ok := socketData.Get(SocketDataKeySubscriptions).([]string); ok {
				for _, subTopic := range subs {
					if subTopic == data.Topic || h.pb.matchesWildcard(subTopic, data.Topic) {
						socket.Send(0, 0, PathMessage, Message{
							Topic:   data.Topic,
							Payload: data.Payload,
						})
						break
					}
				}
			}
		}
		return true
	})

	return map[string]interface{}{
		"code":    "published",
		"message": "发布成功",
	}
}

// Heartbeat 处理客户端的心跳请求
// 服务器直接返回 nil 表示正常响应
func (h *serverHandler) Heartbeat(c *cosnet.Context) interface{} {
	// 服务器收到心跳，直接返回 nil 表示正常响应
	// 可以在这里更新客户端活跃时间等
	return nil
}

// clientHandler 客户端的处理器，接收服务器转发的消息
type clientHandler struct {
	pb *PubSub
}

// Message 接收服务器转发的消息
func (h *clientHandler) Message(c *cosnet.Context) interface{} {
	var data Message
	if err := c.Bind(&data); err != nil {
		return err
	}

	// 将消息发布到本地订阅
	h.pb.processSubscriptions(data.Topic, data.Payload)

	return map[string]interface{}{
		"code":    "received",
		"message": "消息接收成功",
	}
}

// Heartbeat 处理服务器的心跳响应
// 客户端可以不做任何处理，但保留此方法用于扩展
func (h *clientHandler) Heartbeat(c *cosnet.Context) interface{} {
	// 客户端收到服务器的心跳响应
	// 可以在这里处理心跳超时逻辑、重连等
	// 目前不需要做任何处理
	return nil
}
