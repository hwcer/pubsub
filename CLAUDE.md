# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build Commands

```bash
# Build core (zero external dependencies)
go build ./...

# Build cosnet transport sub-module
cd cosnet && go build ./...

# Build redis transport sub-module
cd redis && go build ./...
```

No tests exist yet. The sub-modules (`cosnet/`, `redis/`) are separate Go modules with their own `go.mod` and must be built independently.

## Architecture

This is a publish/subscribe event bus with a pluggable transport layer. The core package has **zero external dependencies** (Go stdlib only). Network and distributed capabilities are provided through separate sub-modules.

### Core Package (`pubsub`)

Three files, stdlib only:

- **`pubsub.go`** — `PubSub` struct with `Subscribe`, `Publish`, `Use` (register transport), `Start`/`Close`. Subscriptions are split into two COW (Copy-On-Write) collections: `exact` (map lookup) for precise topics and `wildcards` (slice scan with precompiled regex) for pattern topics. Writes copy-then-replace under `sync.Mutex`; reads are lock-free.
- **`event.go`** — `Event`, `Handler`, and `Transport` interface. `Event` has unexported `payload any` (local) and `data []byte` (remote). `Unmarshal` tries reflect-based direct assignment first (zero-copy for local), falls back to JSON roundtrip.
- **`wildcard.go`** — `*` matches one `.`-separated segment, `>` matches one or more. Compiled regexps are cached in a `sync.Map`.

### Transport Interface

```go
type Transport interface {
    Start(receiver func(topic string, data []byte)) error
    Close() error
    Publish(topic string, data []byte) error
    Subscribe(topics []string)
    Unsubscribe(topics []string)
}
```

`PubSub.Publish` delivers locally first, then JSON-serializes once and calls each transport's `Publish`. Transports call the `receiver` callback when remote messages arrive, which triggers local delivery with `[]byte` data (JSON-deserialized in `Event.Unmarshal`).

### Sub-module: `cosnet/` — TCP/WebSocket Transport

Depends on `github.com/hwcer/cosnet` and `github.com/hwcer/cosgo`. Two transport types:

- **`ServerTransport`** (`Listen`) — Accepts client connections, tracks per-socket subscription lists in `session.Data`, broadcasts to matching sockets (skipping the source). Handlers are registered on cosnet path `/pubsub/*` via method-name routing (`%m`).
- **`ClientTransport`** (`Connect`) — Connects to server, syncs local subscriptions on connect via `BatchSubscribe`, forwards publishes to server. The server does NOT echo messages back to the originating client.

### Sub-module: `redis/` — Redis Pub/Sub Transport

Depends on `github.com/go-redis/redis/v8`. Uses per-topic Redis channels with a configurable prefix (`prefix:topic`). Messages are wrapped in an `envelope{Origin, Data}` — each transport instance has a unique `id`, and the listener goroutine skips messages where `Origin == self.id` to prevent the publisher from receiving its own broadcast back.

### Message Flow — Key Invariant

`Publish` always delivers locally **exactly once**, regardless of transports. Neither cosnet nor Redis will echo a message back to the originating process. cosnet achieves this by skipping the source socket; Redis achieves this via origin-ID filtering in the envelope.
