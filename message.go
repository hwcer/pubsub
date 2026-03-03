package pubsub

import (
	"regexp"

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

	// COW 模式读取：直接读取，无需加锁
	// 注意：此操作在 64 位架构上是安全的
	exactSubs := ps.exactSubscriptions
	wildcardSubs := ps.wildcardSubscriptions

	// 处理精确匹配（直接通过map查找）
	if sub, ok := exactSubs[topic]; ok {
		ps.sendToSubscribers(sub, msg)
	}

	// 处理通配符匹配（只遍历通配符订阅）
	for _, sub := range wildcardSubs {
		if ps.matchesWildcard(sub.Topic, topic) {
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
	// COW 模式读取：直接读取，无需加锁
	// 注意：此操作在 64 位架构上是安全的
	exactSubs := ps.exactSubscriptions
	wildcardSubs := ps.wildcardSubscriptions

	topics := make([]string, 0, len(exactSubs)+len(wildcardSubs))
	for topic := range exactSubs {
		topics = append(topics, topic)
	}
	for _, sub := range wildcardSubs {
		topics = append(topics, sub.Topic)
	}

	return topics
}

// GetSubscriberCount 获取主题的订阅者数量
func (ps *PubSub) GetSubscriberCount(topic string) int {
	// COW 模式读取：直接读取，无需加锁
	// 注意：此操作在 64 位架构上是安全的
	exactSubs := ps.exactSubscriptions
	wildcardSubs := ps.wildcardSubscriptions

	count := 0

	// 检查精确匹配订阅
	if _, ok := exactSubs[topic]; ok {
		count++
	}
	// 检查通配符订阅
	for _, sub := range wildcardSubs {
		if sub.Topic == topic {
			count++
			break
		}
	}

	return count
}
