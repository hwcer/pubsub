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
	IsServer     bool     `json:"is_server"`     // 是否启动了服务器
	OtherServers []string `json:"other_servers"` // 其他活动的服务器列表
	TreeLevel    int      `json:"tree_level"`    // 在生成树中的层级
}

// HandshakeData 握手数据
type HandshakeData struct {
	ServerInfo ServerInfo `json:"server_info"` // 服务器信息
}

// ResponseData 通用响应数据
type ResponseData struct {
	Code    string      `json:"code"`           // 响应码
	Message string      `json:"message"`        // 响应消息
	Data    interface{} `json:"data,omitempty"` // 响应数据
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
	IsServer           bool              `json:"is_server"`           // 是否是服务器节点
	Subscriptions      []string          `json:"subscriptions"`       // 普通订阅列表
	QueueSubscriptions map[string]string `json:"queue_subscriptions"` // 队列订阅列表
}
