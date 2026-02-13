package pubsub

import (
	"regexp"

	"github.com/hwcer/cosnet"
	"github.com/hwcer/cosnet/listener"
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
	// 使用sync.Map的Load方法获取订阅
	if sub, ok := ps.subscriptions.Load(topic); ok {
		// 直接使用本地订阅
		ps.sendToSubscribers(sub.(Subscription), topic, msg)
	}

	// 处理远程订阅（存储在socket.Data中）
	ps.Range(func(socket *cosnet.Socket) bool {
		if data := socket.Data(); data != nil {
			if connInfo, ok := data.Get("connection_info").(ConnectionInfo); ok {
				// 处理普通订阅
				for _, subTopic := range connInfo.Subscriptions {
					if subTopic == topic {
						socket.Send(0, 0, PathMessage, MessageData{
							Topic:   topic,
							Message: msg,
						})
						break
					}
				}
				// 处理队列订阅
				for subTopic := range connInfo.QueueSubscriptions {
					if subTopic == topic {
						socket.Send(0, 0, PathMessage, MessageData{
							Topic:   topic,
							Message: msg,
						})
						break
					}
				}
			}
		}
		return true
	})
}

// processWildcardSubscriptions 处理通配符匹配的订阅
func (ps *PubSub) processWildcardSubscriptions(topic string, msg interface{}) {
	// 使用sync.Map的Range方法遍历所有订阅
	ps.subscriptions.Range(func(key, value interface{}) bool {
		subTopic := key.(string)
		if subTopic != topic && ps.matchesWildcard(subTopic, topic) {
			// 直接使用本地订阅
			ps.sendToSubscribers(value.(Subscription), topic, msg)
		}
		return true
	})

	// 处理远程订阅（存储在socket.Data中）
	ps.Range(func(socket *cosnet.Socket) bool {
		if data := socket.Data(); data != nil {
			if connInfo, ok := data.Get("connection_info").(ConnectionInfo); ok {
				// 处理普通订阅
				for _, subTopic := range connInfo.Subscriptions {
					if subTopic != topic && ps.matchesWildcard(subTopic, topic) {
						socket.Send(0, 0, PathMessage, MessageData{
							Topic:   topic,
							Message: msg,
						})
						break
					}
				}
				// 处理队列订阅
				for subTopic := range connInfo.QueueSubscriptions {
					if subTopic != topic && ps.matchesWildcard(subTopic, topic) {
						socket.Send(0, 0, PathMessage, MessageData{
							Topic:   topic,
							Message: msg,
						})
						break
					}
				}
			}
		}
		return true
	})
}

// sendToSubscribers 向订阅者发送消息
func (ps *PubSub) sendToSubscribers(sub Subscription, topic string, msg interface{}) {
	// 处理订阅，无论是队列订阅还是普通订阅
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
func (ps *PubSub) GetSubscriptions(socket listener.Socket) []string {
	var topics []string

	// 获取本地订阅
	// 使用sync.Map的Range方法遍历所有订阅
	ps.subscriptions.Range(func(key, value interface{}) bool {
		topics = append(topics, key.(string))
		return true
	})

	// 获取远程订阅（从socket.Data中）
	if data := socket.Data(); data != nil {
		if connInfo, ok := data.Get("connection_info").(ConnectionInfo); ok {
			// 添加普通订阅
			topics = append(topics, connInfo.Subscriptions...)
			// 添加队列订阅
			for topic := range connInfo.QueueSubscriptions {
				topics = append(topics, topic)
			}
		}
	}

	return topics
}

// GetSubscriberCount 获取主题的订阅者数量
func (ps *PubSub) GetSubscriberCount(topic string) int {
	count := 0

	// 统计本地订阅
	// 使用sync.Map的Load方法获取订阅
	if _, ok := ps.subscriptions.Load(topic); ok {
		count++ // 本地订阅算一个
	}

	// 统计远程订阅（从socket.Data中）
	ps.Range(func(socket *cosnet.Socket) bool {
		if data := socket.Data(); data != nil {
			if connInfo, ok := data.Get("connection_info").(ConnectionInfo); ok {
				// 统计普通订阅
				for _, subTopic := range connInfo.Subscriptions {
					if subTopic == topic {
						count++
						break
					}
				}
				// 统计队列订阅
				for subTopic := range connInfo.QueueSubscriptions {
					if subTopic == topic {
						count++
						break
					}
				}
			}
		}
		return true
	})

	return count
}
