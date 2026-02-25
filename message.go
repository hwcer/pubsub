package pubsub

import (
	"regexp"

	"github.com/hwcer/cosnet"
)

// processSubscriptions 处理订阅（包括本地订阅和远程订阅）
func (ps *PubSub) processSubscriptions(topic string, msg interface{}) {
	// 处理精确匹配的订阅
	ps.processExactSubscriptions(topic, msg)

	// 处理通配符匹配的订阅
	ps.processWildcardSubscriptions(topic, msg)
}

// processExactSubscriptions 处理精确匹配的订阅
func (ps *PubSub) processExactSubscriptions(topic string, msg interface{}) {
	// 处理本地订阅
	ps.mutex.RLock()
	sub, ok := ps.subscriptions[topic]
	ps.mutex.RUnlock()

	if ok {
		ps.sendToSubscribers(sub, topic, msg)
	}

	// 处理远程订阅（仅服务器模式）
	ps.broadcastToRemote(topic, msg, false)
}

// processWildcardSubscriptions 处理通配符匹配的订阅
func (ps *PubSub) processWildcardSubscriptions(topic string, msg interface{}) {
	// 处理本地通配符订阅
	ps.mutex.RLock()
	subs := make([]*LocalSubscription, 0, len(ps.subscriptions))
	for _, sub := range ps.subscriptions {
		subs = append(subs, sub)
	}
	ps.mutex.RUnlock()

	for _, sub := range subs {
		if sub.Topic != topic && ps.matchesWildcard(sub.Topic, topic) {
			ps.sendToSubscribers(sub, topic, msg)
		}
	}

	// 处理远程通配符订阅（仅服务器模式）
	ps.broadcastToRemote(topic, msg, true)
}

// broadcastToRemote 向远程客户端广播消息
// wildcard: 是否匹配通配符
func (ps *PubSub) broadcastToRemote(topic string, msg interface{}, wildcard bool) {
	if !ps.Is(ModeServer) || ps.sockets == nil {
		return
	}

	ps.sockets.Range(func(socket *cosnet.Socket) bool {
		if ps.shouldSendToSocket(socket, topic, wildcard) {
			socket.Send(0, 0, PathMessage, Message{
				Topic:   topic,
				Payload: msg,
			})
		}
		return true
	})
}

// shouldSendToSocket 检查是否应该向该socket发送消息
func (ps *PubSub) shouldSendToSocket(socket *cosnet.Socket, topic string, wildcard bool) bool {
	data := socket.Data()
	if data == nil {
		return false
	}

	// 检查订阅
	if subs, ok := data.Get(SocketDataKeySubscriptions).([]string); ok {
		for _, subTopic := range subs {
			if ps.topicMatches(subTopic, topic, wildcard) {
				return true
			}
		}
	}

	return false
}

// topicMatches 检查订阅主题是否匹配消息主题
func (ps *PubSub) topicMatches(subTopic, msgTopic string, wildcard bool) bool {
	if !wildcard {
		// 精确匹配
		return subTopic == msgTopic
	}
	// 通配符匹配（排除精确匹配的情况）
	return subTopic != msgTopic && ps.matchesWildcard(subTopic, msgTopic)
}

// sendToSubscribers 向订阅者发送消息
func (ps *PubSub) sendToSubscribers(sub *LocalSubscription, topic string, msg interface{}) {
	if len(sub.Handlers) > 0 {
		for _, handler := range sub.Handlers {
			handler(topic, msg)
		}
	}
}

// matchesWildcard 检查主题是否匹配通配符
func (ps *PubSub) matchesWildcard(subTopic, topic string) bool {
	rePattern := subTopic
	rePattern = regexp.QuoteMeta(rePattern)
	rePattern = regexp.MustCompile(`\*`).ReplaceAllString(rePattern, `[^.]+`)
	rePattern = regexp.MustCompile(`\>`).ReplaceAllString(rePattern, `.+`)
	rePattern = `^` + rePattern + `$`

	matched, _ := regexp.MatchString(rePattern, topic)
	return matched
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
