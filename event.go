package pubsub

import (
	"encoding/json"
	"errors"
	"reflect"
)

// Handler 订阅回调函数
type Handler func(event *Event)

// Event 发布的事件，本地发布时 payload 为原始 Go 对象，远程接收时 data 为 JSON 字节
type Event struct {
	Topic   string
	payload any
	data    []byte
}

func newLocalEvent(topic string, payload any) *Event {
	return &Event{Topic: topic, payload: payload}
}

func newRemoteEvent(topic string, data []byte) *Event {
	return &Event{Topic: topic, data: data}
}

func (e *Event) Unmarshal(v any) error {
	if v == nil {
		return errors.New("target cannot be nil")
	}
	if e.data != nil {
		return json.Unmarshal(e.data, v)
	}
	if e.payload == nil {
		return nil
	}
	dst := reflect.ValueOf(v)
	if dst.Kind() != reflect.Ptr || dst.IsNil() {
		return errors.New("target must be a non-nil pointer")
	}
	src := reflect.ValueOf(e.payload)
	if src.Kind() == reflect.Ptr {
		if src.IsNil() {
			return nil
		}
		src = src.Elem()
	}
	if src.Type().AssignableTo(dst.Elem().Type()) {
		dst.Elem().Set(src)
		return nil
	}
	b, err := json.Marshal(e.payload)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// Transport 传输层接口，用于分布式消息分发
type Transport interface {
	// Start 启动传输层，receiver 用于接收远程消息
	Start(receiver func(topic string, data []byte)) error
	// Close 关闭传输层
	Close() error
	// Publish 将消息发送到远程节点
	Publish(topic string, data []byte) error
	// Subscribe 通知传输层本地新增了订阅
	Subscribe(topics []string)
	// Unsubscribe 通知传输层本地移除了订阅
	Unsubscribe(topics []string)
}
