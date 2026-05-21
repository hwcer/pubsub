# PubSub

Go 语言发布/订阅事件总线，核心零依赖，支持通配符订阅和可插拔传输层。

## 安装

```bash
# 核心包（零外部依赖）
go get github.com/hwcer/pubsub

# Redis 传输层
go get github.com/hwcer/pubsub/redis

# cosnet 网络传输层
go get github.com/hwcer/pubsub/cosnet
```

## 快速开始

### 本地事件总线

核心包仅依赖 Go 标准库，无需任何外部依赖。

```go
package main

import (
    "fmt"
    "github.com/hwcer/pubsub"
)

type UserEvent struct {
    UID    int64  `json:"uid"`
    Action string `json:"action"`
}

func main() {
    ps := pubsub.New()

    ps.Subscribe("user.login", func(event *pubsub.Event) {
        var msg UserEvent
        event.Unmarshal(&msg)
        fmt.Printf("user %d: %s\n", msg.UID, msg.Action)
    })

    ps.Publish("user.login", &UserEvent{UID: 1001, Action: "login"})
}
```

### 使用 Redis 分布式传输

多进程/多服务之间共享事件，使用 per-topic Redis channel。

```go
package main

import (
    "github.com/go-redis/redis/v8"
    "github.com/hwcer/pubsub"
    psredis "github.com/hwcer/pubsub/redis"
)

func main() {
    ps := pubsub.New()

    client := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
    ps.Use(psredis.New(client, "myapp"))

    ps.Subscribe("order.created", func(event *pubsub.Event) {
        // 本地和远程消息都会到达这里
    })

    ps.Start()

    // 本地分发 + 通过 Redis 广播到其他进程
    ps.Publish("order.created", map[string]any{"id": "ORD-001", "amount": 99.9})
}
```

### 使用 cosnet 网络传输

基于 TCP/WebSocket 的服务端-客户端模式。

```go
// 服务端
ps := pubsub.New()
ps.Use(pscosnet.Listen(":9000"))
ps.Start()

// 客户端
ps := pubsub.New()
ps.Use(pscosnet.Connect("server:9000"))
ps.Subscribe("events.>", handler)
ps.Start()
```

## 通配符订阅

主题使用 `.` 分隔层级，支持两种通配符：

| 通配符 | 含义 | 示例模式 | 匹配 | 不匹配 |
|--------|------|----------|------|--------|
| `*` | 单层级 | `user.*.login` | `user.alice.login` | `user.login`, `user.a.b.login` |
| `>` | 一个或多个层级 | `user.>` | `user.login`, `user.a.b.c` | `user` |

```go
// 所有用户事件
ps.Subscribe("user.>", handler)

// 任意服务的日志
ps.Subscribe("*.logs", handler)

// 精确匹配和通配符匹配的订阅者都会收到消息
```

## 架构

```
pubsub (核心)          仅 Go 标准库，无外部依赖
├── pubsub/cosnet      cosnet 网络传输（TCP/WebSocket 服务端-客户端）
└── pubsub/redis       Redis Pub/Sub 传输（跨进程分布式）
```

### 消息流

**发布 (Publish)**

1. 在本地创建 `Event`（payload 为原始 Go 对象），分发给匹配的本地 Handler
2. 若存在传输层，将 payload JSON 序列化后调用 `transport.Publish(topic, bytes)`

**远程接收**

1. 传输层收到远程消息，调用 `receiver(topic, bytes)`
2. 核心创建 `Event`（data 为 JSON 字节），分发给匹配的本地 Handler
3. Handler 调用 `event.Unmarshal(&target)` 时自动进行 JSON 反序列化

**本地零拷贝**

本地发布时，`Event.Unmarshal` 会尝试直接赋值（反射），类型匹配时跳过 JSON 序列化/反序列化，避免不必要的开销。

**消息去重**

`Publish` 保证本地恰好分发一次，不会因传输层回传导致重复：
- **cosnet**：服务端广播时跳过消息来源 socket
- **Redis**：消息包装为 `envelope{Origin, Data}`，每个实例有唯一 ID，接收时过滤掉自身发出的消息

### 并发模型

- 订阅列表使用 **COW (Copy-On-Write)** 模式：读取无锁，写入复制后替换
- 所有公开方法线程安全

## API

### 核心类型

```go
// Handler 订阅回调
type Handler func(event *Event)

// Event 发布的事件
type Event struct {
    Topic string          // 主题名称
}
func (e *Event) Unmarshal(v any) error  // 反序列化 payload 到目标对象

// Transport 传输层接口
type Transport interface {
    Start(receiver func(topic string, data []byte)) error
    Close() error
    Publish(topic string, data []byte) error
    Subscribe(topics []string)
    Unsubscribe(topics []string)
}
```

### PubSub 方法

```go
func New() *PubSub                                // 创建事件总线
func (ps *PubSub) Use(t Transport)                // 注册传输层（Start 前调用）
func (ps *PubSub) Start() error                   // 启动所有传输层
func (ps *PubSub) Close() error                   // 关闭所有传输层
func (ps *PubSub) Subscribe(topic string, h Handler)  // 订阅主题
func (ps *PubSub) Unsubscribe(topic string)        // 取消订阅
func (ps *PubSub) Publish(topic string, payload any)   // 发布消息
func (ps *PubSub) GetSubscriptions() []string      // 获取所有已订阅主题
func (ps *PubSub) GetSubscriberCount(topic string) int // 获取主题订阅数
```

### Redis 传输层

```go
import psredis "github.com/hwcer/pubsub/redis"

// New 创建 Redis 传输层
// prefix 用于 Redis channel 命名隔离，channel 格式为 "prefix:topic"
func New(client *redis.Client, prefix string) *Transport
```

### cosnet 传输层

```go
import pscosnet "github.com/hwcer/pubsub/cosnet"

func Listen(address string) *ServerTransport   // 创建服务端传输
func Connect(address string) *ClientTransport  // 创建客户端传输
```

## 自定义传输层

实现 `pubsub.Transport` 接口即可接入任意消息中间件：

```go
type MyTransport struct { /* ... */ }

func (t *MyTransport) Start(receiver func(topic string, data []byte)) error {
    // 保存 receiver，启动消息接收循环
    // 收到远程消息时调用 receiver(topic, data)
}

func (t *MyTransport) Close() error { /* 关闭连接 */ }

func (t *MyTransport) Publish(topic string, data []byte) error {
    // 将消息发送到远程
}

func (t *MyTransport) Subscribe(topics []string) {
    // 通知远程本地新增了订阅（可选实现）
}

func (t *MyTransport) Unsubscribe(topics []string) {
    // 通知远程本地移除了订阅（可选实现）
}

// 使用
ps := pubsub.New()
ps.Use(&MyTransport{})
ps.Start()
```
