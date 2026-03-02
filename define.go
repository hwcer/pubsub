package pubsub

import (
	"encoding/json"
	"errors"
	"reflect"

	"github.com/hwcer/cosgo/values"
)

var (
	ErrClientOnly         = values.Error("this operation is only available for clients")
	ErrServerOnly         = values.Error("this operation is only available for servers")
	ErrNotInitialized     = values.Error("client not initialized with NetHub")
	ErrAlreadyInitialized = values.Error("pubsub already initialized, cannot change mode")
)

// socket.Data 存储的 key 常量
const (
	SocketDataKeySubscriptions = "subscriptions" // 订阅列表
)

const (
	BasePath           = "/pubsub"
	PathSubscribe      = BasePath + "/subscribe"
	PathBatchSubscribe = BasePath + "/batch_subscribe"
	PathUnsubscribe    = BasePath + "/unsubscribe"
	PathPublish        = BasePath + "/publish"
	PathMessage        = BasePath + "/message"
	PathHeartbeat      = BasePath + "/heartbeat"
)

type SubscribeHandle func(msg Message)

type Message interface {
	GetTopic() string
	Unmarshal(i any) error
}

// Response 用于接收网络消息并转换为本地Message
// 确保 Payload 类型一致性，并且不会多次序列化
type Response struct {
	Topic   string       `json:"topic"` // 主题
	Payload values.Bytes `json:"payload"`
}

// GetTopic 获取主题
func (r *Response) GetTopic() string {
	return r.Topic
}

func (r *Response) Unmarshal(i any) error {
	return r.Payload.Unmarshal(i)
}

// Request 消息
type Request struct {
	Topic   string `json:"topic"`   // 主题
	Payload any    `json:"payload"` // 消息内容，根据订阅类型不同而不同, 本地订阅时是对象, 远程订阅时是 JSON 二进制,只能使用Unmarshal来获取
}

// GetTopic 获取主题
func (m *Request) GetTopic() string {
	return m.Topic
}

// Unmarshal 反序列化消息内容
// 本地订阅时 Payload 是对象
// 远程订阅时 Payload JSON 二进制，需要反序列化
func (m *Request) Unmarshal(i any) error {
	if i == nil {
		return errors.New("target cannot be nil")
	}

	// 检查 Payload 类型
	switch payload := m.Payload.(type) {
	case []byte:
		// 远程订阅，Payload 是 JSON 二进制，需要反序列化
		return json.Unmarshal(payload, i)
	default:
		// 检查 i 是否是指针
		v := reflect.ValueOf(i)
		if v.Kind() != reflect.Ptr {
			return errors.New("target must be a pointer")
		}
		// 本地订阅，Payload 是对象本身，直接将指针指向 Payload
		targetValue := reflect.ValueOf(payload)

		// 如果 targetValue 是指针，解引用获取实际值
		if targetValue.Kind() == reflect.Ptr {
			if targetValue.IsNil() {
				return errors.New("payload pointer is nil")
			}
			targetValue = targetValue.Elem()
		}

		if targetValue.Type().AssignableTo(v.Elem().Type()) {
			v.Elem().Set(targetValue)
			return nil
		}
		// 如果类型不匹配，尝试转换为 JSON 再反序列化
		jsonData, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		return json.Unmarshal(jsonData, i)
	}
}

// Subscription 订阅请求
type Subscription struct {
	Topic string `json:"topic"` // 主题
}

// Unsubscription 取消订阅请求
type Unsubscription struct {
	Topics []string `json:"topics"` // 主题列表
}

// BatchSubscription 批量订阅请求
type BatchSubscription struct {
	Topics []string `json:"topics"` // 主题列表
}
