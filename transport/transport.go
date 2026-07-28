// Package transport 按地址协议创建传输层，屏蔽各实现的差异与调参细节。
//
// 核心包 pubsub 不能直接依赖各传输层（cosnet / redis 反过来依赖它，会成环），
// 所以协议分发放在这个独立子模块里。
//
//	tcp://host:port    cosnet TCP，scheme 省略时的默认值
//	redis://host:port  redis pub/sub
package transport

import (
	"errors"
	"fmt"
	"strings"

	"github.com/go-redis/redis/v8"
	"github.com/hwcer/pubsub"
	pscosnet "github.com/hwcer/pubsub/cosnet"
	psredis "github.com/hwcer/pubsub/redis"
)

const (
	SchemeTCP    = "tcp"
	SchemeRedis  = "redis"
	SchemeRediss = "rediss" //redis over TLS
)

// 客户端重连参数。cosnet 的默认值对事件总线不合适：
// 重连上限 10 次（约 3 分钟）后彻底放弃，表现为永久静默失联；
// 退避封顶 30 秒，服务端重启后最坏要空档 30 秒才恢复，期间发布的消息全丢。
// 这类连接平时零流量、重连成本极低，这里统一改成无限重连 + 0.5 秒起步、最多 3 秒。
const (
	clientReconnectMax      = 0
	clientReconnectTime     = 500
	clientReconnectMaxDelay = 3000
)

type config struct {
	readOnly bool
	prefix   string
}

type Option func(*config)

// ReadOnly 服务端忽略客户端发来的 publish，仅单向下发。
// 仅对 tcp 生效：cosnet 服务端对连入的 socket 无条件放行，不开只读的话
// 任何能连到该端口的客户端都能把消息注入本进程的事件总线。
func ReadOnly() Option {
	return func(c *config) { c.readOnly = true }
}

// Prefix redis channel 名前缀，用于同一实例上隔离不同业务。仅对 redis 生效。
func Prefix(s string) Option {
	return func(c *config) { c.prefix = s }
}

// Listen 创建服务端传输层。
//
// redis 没有服务端概念，redis:// 地址下与 Connect 等价，
// ReadOnly 也随之失效——任何能连到该 redis 的进程都能往 topic 发消息，
// 需要靠 redis 自身的 ACL 或网络隔离来防。
func Listen(address string, opts ...Option) (pubsub.Transport, error) {
	scheme, addr, err := split(address)
	if err != nil {
		return nil, err
	}
	cfg := parse(opts)
	switch scheme {
	case SchemeTCP:
		t := pscosnet.Listen(addr)
		t.ReadOnly(cfg.readOnly)
		return t, nil
	case SchemeRedis, SchemeRediss:
		return dialRedis(address, cfg)
	default:
		return nil, fmt.Errorf("pubsub: unsupported transport scheme:%v", scheme)
	}
}

// Connect 创建客户端传输层。
func Connect(address string, opts ...Option) (pubsub.Transport, error) {
	scheme, addr, err := split(address)
	if err != nil {
		return nil, err
	}
	cfg := parse(opts)
	switch scheme {
	case SchemeTCP:
		t := pscosnet.Connect(addr)
		o := t.Options()
		o.ClientReconnectMax = clientReconnectMax
		o.ClientReconnectTime = clientReconnectTime
		o.ClientReconnectMaxDelay = clientReconnectMaxDelay
		return t, nil
	case SchemeRedis, SchemeRediss:
		return dialRedis(address, cfg)
	default:
		return nil, fmt.Errorf("pubsub: unsupported transport scheme:%v", scheme)
	}
}

func dialRedis(address string, cfg *config) (pubsub.Transport, error) {
	opts, err := redis.ParseURL(address)
	if err != nil {
		return nil, fmt.Errorf("pubsub: parse redis url:%w", err)
	}
	return psredis.New(redis.NewClient(opts), cfg.prefix), nil
}

// split 拆出协议与地址，未带 scheme 时按 tcp 处理。
// 注意 cosnet 只要 host:port，不要把 tcp:// 前缀带进去。
func split(address string) (scheme, addr string, err error) {
	if address == "" {
		return "", "", errors.New("pubsub: transport address is empty")
	}
	if i := strings.Index(address, "://"); i >= 0 {
		return strings.ToLower(address[:i]), address[i+3:], nil
	}
	return SchemeTCP, address, nil
}

func parse(opts []Option) *config {
	cfg := &config{}
	for _, o := range opts {
		o(cfg)
	}
	return cfg
}
