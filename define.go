package pubsub

import "errors"

var (
	ErrClientOnly         = errors.New("this operation is only available for clients")
	ErrServerOnly         = errors.New("this operation is only available for servers")
	ErrNotInitialized     = errors.New("client not initialized with NetHub")
	ErrAlreadyInitialized = errors.New("pubsub already initialized, cannot change mode")
)

const (
	BasePath           = "/pubsub"
	PathSubscribe      = BasePath + "/subscribe"
	PathBatchSubscribe = BasePath + "/batch_subscribe"
	PathUnsubscribe    = BasePath + "/unsubscribe"
	PathPublish        = BasePath + "/publish"
	PathMessage        = BasePath + "/message"
)

// Message 消息
type Message struct {
	Topic   string      `json:"topic"`   // 主题
	Payload interface{} `json:"payload"` // 消息内容
}

// Subscription 订阅请求
type Subscription struct {
	Topic string `json:"topic"` // 主题
}

// Unsubscription 取消订阅请求
type Unsubscription struct {
	Topics []string `json:"topics"` // 主题列表
}

// Publication 发布请求
type Publication struct {
	Topic   string      `json:"topic"`   // 主题
	Payload interface{} `json:"payload"` // 消息内容
}

// BatchSubscription 批量订阅请求
type BatchSubscription struct {
	Topics []string `json:"topics"` // 主题列表
}

// socket.Data 存储的 key 常量
const (
	SocketDataKeySubscriptions = "subscriptions" // 订阅列表
)
