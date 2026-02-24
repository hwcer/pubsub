# PubSub 发布订阅系统

基于 Go 实现的发布订阅系统，支持客户端-服务器模式，提供通配符订阅功能。

## 特性

- **客户端-服务器架构**：服务器管理客户端连接，客户端可连接多个服务器
- **通配符订阅**：支持 `*` 和 `>` 通配符匹配主题
- **批量订阅**：支持一次性订阅多个主题
- **自动重连**：客户端自动同步订阅到远程服务器

## 快速开始

### 创建服务器

```go
package main

import (
    "log"
    "github.com/hwcer/pubsub"
)

func main() {
    // 方式1：快速创建并启动
    server, err := pubsub.NewServer(":8080")
    if err != nil {
        log.Fatal(err)
    }
    
    // 方式2：手动创建
    ps := pubsub.New()
    err := ps.Listen(":8080")
    if err != nil {
        log.Fatal(err)
    }
    
    // 服务器也可以订阅本地消息
    ps.Subscribe("server.logs", func(topic string, msg interface{}) {
        log.Printf("[Server] %s: %v", topic, msg)
    })
    
    // 启动服务
    server.Start()
}
```

### 创建客户端

```go
package main

import (
    "log"
    "time"
    "github.com/hwcer/pubsub"
)

func main() {
    // 方式1：快速创建并连接
    client, err := pubsub.NewClient("localhost:8080")
    if err != nil {
        log.Fatal(err)
    }
    
    // 方式2：手动创建
    ps := pubsub.New()
    err := ps.Connect("localhost:8080")
    if err != nil {
        log.Fatal(err)
    }
    
    // 订阅主题
    ps.Subscribe("user.login", func(topic string, msg interface{}) {
        log.Printf("[Client] %s: %v", topic, msg)
    })
    
    // 发布消息
    ps.Publish("user.login", map[string]string{
        "username": "alice",
        "time": time.Now().String(),
    })
    
    select {} // 保持运行
}
```

## 通配符订阅（模糊订阅）

系统支持两种通配符，让你可以灵活地订阅一类主题：

### 通配符类型

| 通配符 | 含义 | 示例 |
|--------|------|------|
| `*` | 匹配单个层级（任意字符，除点号） | `user.*.profile` |
| `>` | 匹配一个或多个层级 | `user.>` |

### 主题层级

主题使用点号（`.`）分隔层级，例如：
- `user.login`
- `user.profile.update`
- `order.created`
- `order.status.changed`

### 使用示例

#### 1. 单层级通配符 `*`

匹配单个层级的任意值。

```go
// 订阅所有用户的登录事件
ps.Subscribe("user.*.login", func(topic string, msg interface{}) {
    log.Printf("用户登录: %s", topic)
    // 匹配: user.alice.login, user.bob.login
    // 不匹配: user.login, user.alice.profile.login
})

// 订阅所有服务的日志
ps.Subscribe("*.logs", func(topic string, msg interface{}) {
    log.Printf("日志: %s - %v", topic, msg)
    // 匹配: api.logs, worker.logs, db.logs
})
```

#### 2. 多层级通配符 `>`

匹配一个或多个层级的任意值。

```go
// 订阅所有用户相关事件
ps.Subscribe("user.>", func(topic string, msg interface{}) {
    log.Printf("用户事件: %s", topic)
    // 匹配: 
    //   user.login
    //   user.alice.login
    //   user.profile.update
    //   user.alice.profile.update
})

// 订阅所有订单状态变更
ps.Subscribe("order.>.status", func(topic string, msg interface{}) {
    log.Printf("订单状态: %s - %v", topic, msg)
    // 匹配:
    //   order.123.status
    //   order.abc.def.status
})
```

#### 3. 组合使用

```go
// 订阅所有区域的所有服务日志
ps.Subscribe("*.*.logs", func(topic string, msg interface{}) {
    // 匹配: prod.api.logs, dev.worker.logs, test.db.logs
})

// 订阅某个区域的所有事件
ps.Subscribe("prod.>", func(topic string, msg interface{}) {
    // 匹配: prod.api.start, prod.db.connected, prod.worker.job.done
})
```

### 匹配规则总结

```
订阅主题: user.*.login
匹配:     user.alice.login, user.bob.login
不匹配:   user.login, user.alice.profile.login

订阅主题: user.>
匹配:     user.login, user.alice.login, user.alice.profile.update
不匹配:   user, admin.user.login

订阅主题: *.logs
匹配:     api.logs, db.logs
不匹配:   logs, prod.api.logs

订阅主题: order.*.status
匹配:     order.123.status, order.abc.status
不匹配:   order.status, order.123.456.status
```

### 注意事项

1. **层级分隔**：主题使用点号 `.` 分隔层级
2. **通配符位置**：`*` 和 `>` 可以出现在主题的任意位置
3. **精确匹配优先**：如果同时订阅了精确主题和通配符主题，两者都会收到消息
4. **性能考虑**：过于宽泛的通配符（如 `>`）可能导致消息分发到更多订阅者

### 实际应用场景

```go
// 场景1：监控所有 API 请求
ps.Subscribe("api.>.request", func(topic string, msg interface{}) {
    // 记录所有 API 请求日志
    metrics.RecordAPICall(topic, msg)
})

// 场景2：用户通知系统
ps.Subscribe("notify.user.*", func(topic string, msg interface{}) {
    // 发送用户通知
    userID := extractUserID(topic) // 从 topic 解析用户ID
    sendNotification(userID, msg)
})

// 场景3：订单全生命周期监控
ps.Subscribe("order.>", func(topic string, msg interface{}) {
    // 记录订单所有状态变更
    orderLogger.Log(topic, msg)
})

// 场景4：特定服务的所有事件
ps.Subscribe("*.payment.>", func(topic string, msg interface{}) {
    // 处理所有支付相关事件
    processPaymentEvent(topic, msg)
})
```

## API 参考

### 类型定义

```go
// WorkMode 工作模式
type WorkMode int

const (
    ModeUnknown WorkMode = iota // 未确定模式
    ModeClient                  // 客户端模式
    ModeServer                  // 服务器模式
)
```

### 主要方法

#### 创建实例

```go
// New 创建一个新的 PubSub 实例
func New() *PubSub

// NewServer 快速创建并启动服务器
func NewServer(address string) (*PubSub, error)

// NewClient 快速创建客户端并连接到服务器
func NewClient(address string) (*PubSub, error)
```

#### 服务器方法

```go
// Listen 启动服务器监听
func (ps *PubSub) Listen(address string) error

// Start 启动服务（阻塞）
func (ps *PubSub) Start() error
```

#### 客户端方法

```go
// Connect 连接到服务器
func (ps *PubSub) Connect(address string) error
```

#### 订阅与发布

```go
// Subscribe 订阅主题
func (ps *PubSub) Subscribe(topic string, handler func(topic string, msg interface{}))

// Unsubscribe 取消订阅主题
func (ps *PubSub) Unsubscribe(topic string)

// UnsubscribeAll 取消所有订阅
func (ps *PubSub) UnsubscribeAll()

// Publish 发布消息到主题
func (ps *PubSub) Publish(topic string, msg interface{})
```

#### 查询方法

```go
// GetSubscriptions 获取所有本地订阅的主题列表
func (ps *PubSub) GetSubscriptions() []string

// GetSubscriberCount 获取主题的订阅者数量
func (ps *PubSub) GetSubscriberCount(topic string) int

// Is 判断当前是否是指定的工作模式
func (ps *PubSub) Is(mode WorkMode) bool
```

## 完整示例

```go
package main

import (
    "log"
    "time"
    "github.com/hwcer/pubsub"
)

func main() {
    // 启动服务器
    server, err := pubsub.NewServer(":8080")
    if err != nil {
        log.Fatal(err)
    }
    
    // 服务器订阅所有用户事件（通配符）
    server.Subscribe("user.>", func(topic string, msg interface{}) {
        log.Printf("[Server] 用户事件: %s = %v", topic, msg)
    })
    
    go server.Start()
    
    time.Sleep(100 * time.Millisecond)
    
    // 创建客户端
    client, err := pubsub.NewClient("localhost:8080")
    if err != nil {
        log.Fatal(err)
    }
    
    // 客户端订阅特定主题
    client.Subscribe("user.login", func(topic string, msg interface{}) {
        log.Printf("[Client] 登录: %v", msg)
    })
    
    // 客户端订阅通配符主题
    client.Subscribe("user.*.profile", func(topic string, msg interface{}) {
        log.Printf("[Client] 资料更新: %v", msg)
    })
    
    time.Sleep(100 * time.Millisecond)
    
    // 发布消息
    client.Publish("user.login", "alice")
    client.Publish("user.alice.profile", map[string]int{"age": 25})
    client.Publish("user.bob.login", "bob")
    
    time.Sleep(1 * time.Second)
}
```

## 注意事项

1. **线程安全**：所有方法都是线程安全的，可以在多个 goroutine 中并发调用
2. **消息顺序**：同一主题的消息会按发布顺序传递给订阅者
3. **错误处理**：连接错误会自动重连，订阅会自动同步
4. **资源释放**：程序退出时会自动清理资源
