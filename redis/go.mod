module github.com/hwcer/pubsub/redis

go 1.25.0

// 不要在这里 replace 核心包:replace 只对主模块生效,下游 go get 时会忽略它并去解析
// require 的真实版本。本地要同时改核心包与本子模块时用 go work,不要把 replace 写回。
require (
	github.com/go-redis/redis/v8 v8.11.5
	github.com/hwcer/pubsub v0.0.0-20260728075544-f351c68a65dc
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	github.com/hwcer/logger v0.2.9-0.20260626033726-42e0a5927245 // indirect
	golang.org/x/net v0.54.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
)
