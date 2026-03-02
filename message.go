package pubsub

import (
	"github.com/hwcer/cosnet"
)

// processSubscriptions 处理订阅（包括本地订阅和远程订阅）
// source: 来源socket，为nil时广播给所有客户端，否则跳过来源
func (ps *PubSub) processSubscriptions(msg Message, source *cosnet.Socket) {
	// 处理本地订阅（包括精确匹配和通配符匹配）
	ps.processLocalSubscriptions(msg)

	// 处理远程订阅（仅服务器模式）
	ps.broadcastToRemote(msg, source)
}

// processLocalSubscriptions 处理本地订阅（包括精确匹配和通配符匹配）
func (ps *PubSub) processLocalSubscriptions(msg Message) {
	topic := msg.GetTopic()

	ps.mutex.RLock()
	defer ps.mutex.RUnlock()

	// 一次遍历同时处理精确匹配和通配符匹配
	for _, sub := range ps.subscriptions {
		if sub.Topic == topic || ps.matchesWildcard(sub.Topic, topic) {
			ps.sendToSubscribers(sub, msg)
		}
	}
}

// broadcastToRemote 向远程客户端广播消息
// source: 来源socket，为nil时广播给所有客户端，否则跳过来源
func (ps *PubSub) broadcastToRemote(msg Message, source *cosnet.Socket) {
	if !ps.Is(ModeServer) || ps.sockets == nil {
		return
	}

	topic := msg.GetTopic()

	ps.sockets.Range(func(socket *cosnet.Socket) bool {
		// 跳过来源socket
		if socket.Is(source) {
			return true
		}
		if ps.shouldSendToSocket(socket, topic) {
			socket.Send(0, 0, PathMessage, msg)
		}
		return true
	})
}

// shouldSendToSocket 检查是否应该向该socket发送消息
func (ps *PubSub) shouldSendToSocket(socket *cosnet.Socket, topic string) bool {
	data := socket.Data()
	if data == nil {
		return false
	}

	// 检查订阅
	if subs, ok := data.Get(SocketDataKeySubscriptions).([]string); ok {
		for _, subTopic := range subs {
			// 检查精确匹配
			if subTopic == topic {
				return true
			}
			// 检查通配符匹配
			if ps.matchesWildcard(subTopic, topic) {
				return true
			}
		}
	}

	return false
}

// sendToSubscribers 向订阅者发送消息
func (ps *PubSub) sendToSubscribers(sub *LocalSubscription, msg Message) {
	if len(sub.Handlers) > 0 {
		for _, handler := range sub.Handlers {
			handler(msg)
		}
	}
}

// matchesWildcard 检查主题是否匹配通配符（使用缓存优化）
func (ps *PubSub) matchesWildcard(subTopic, topic string) bool {
	re := ps.compileWildcardPattern(subTopic)
	return re.MatchString(topic)
}

// GetSubscriptions 获取所有本地订阅的主题列表
func (ps *PubSub) GetSubscriptions() []string {
	ps.mutex.RLock()
	defer ps.mutex.RUnlock()

	topics := make([]string, 0, len(ps.subscriptions))
	for topic := range ps.subscriptions {
		topics = append(topics, topic)
	}

	return topics
}

// GetSubscriberCount 获取主题的订阅者数量
func (ps *PubSub) GetSubscriberCount(topic string) int {
	count := 0

	// 统计本地订阅
	ps.mutex.RLock()
	if _, ok := ps.subscriptions[topic]; ok {
		count++
	}
	ps.mutex.RUnlock()

	return count
}
