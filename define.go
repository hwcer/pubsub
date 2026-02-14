package pubsub

const (
	BasePath           = "/pubsub"
	PathHandshake      = BasePath + "/handshake"
	PathSubscribe      = BasePath + "/subscribe"
	PathQueueSubscribe = BasePath + "/queue_subscribe"
	PathUnsubscribe    = BasePath + "/unsubscribe"
	PathPublish        = BasePath + "/publish"
	PathMessage        = BasePath + "/message"
	PathSubscribeList  = BasePath + "/subscribe_list"
)

// ServerInfo 服务器信息
type ServerInfo struct {
	Address      string   `json:"address"`       // 服务器地址
	OtherServers []string `json:"other_servers"` // 其他活动的服务器列表
}

// MessageData 消息数据
type MessageData struct {
	Topic   string      `json:"topic"`   // 主题
	Message interface{} `json:"message"` // 消息内容
}

// SubscribeListData 订阅列表数据
type SubscribeListData struct {
	Topics []string `json:"topics"` // 主题列表
}

// ConnectionInfo 连接信息
// 存储在socket.Data中，包含订阅信息和服务器地址信息
type ConnectionInfo struct {
	Address            string            `json:"address"`             // 服务器地址（如果是服务器的话）
	Subscriptions      []string          `json:"subscriptions"`       // 普通订阅列表
	QueueSubscriptions map[string]string `json:"queue_subscriptions"` // 队列订阅列表
}

func (c *ConnectionInfo) IsServer() bool {
	return c.Address != ""
}
