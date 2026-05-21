package cosnet

import (
	"github.com/hwcer/cosnet"
)

const (
	socketDataKeySubscriptions = "subscriptions"

	basePath           = "/pubsub"
	pathSubscribe      = basePath + "/subscribe"
	pathBatchSubscribe = basePath + "/batch_subscribe"
	pathUnsubscribe    = basePath + "/unsubscribe"
	pathPublish        = basePath + "/publish"
	pathMessage        = basePath + "/message"
)

type subscriptionReq struct {
	Topic string `json:"topic"`
}

type unsubscriptionReq struct {
	Topics []string `json:"topics"`
}

type batchSubscriptionReq struct {
	Topics []string `json:"topics"`
}

type publishReq struct {
	Topic string `json:"topic"`
	Data  []byte `json:"data"`
}

// serverHandler 服务端消息处理器
type serverHandler struct {
	transport *ServerTransport
}

func (h *serverHandler) getSubscriptions(c *cosnet.Context) []string {
	if data := c.Socket.Data(); data != nil {
		if subs, ok := data.Get(socketDataKeySubscriptions).([]string); ok {
			return subs
		}
	}
	return nil
}

func (h *serverHandler) setSubscriptions(c *cosnet.Context, subs []string) {
	if data := c.Socket.Data(); data != nil {
		data.Set(socketDataKeySubscriptions, subs)
	}
}

func (h *serverHandler) BatchSubscribe(c *cosnet.Context) any {
	var data batchSubscriptionReq
	if err := c.Bind(&data); err != nil {
		return err
	}
	existing := h.getSubscriptions(c)
	set := make(map[string]bool, len(existing))
	for _, s := range existing {
		set[s] = true
	}
	for _, topic := range data.Topics {
		if !set[topic] {
			existing = append(existing, topic)
			set[topic] = true
		}
	}
	h.setSubscriptions(c, existing)
	return nil
}

func (h *serverHandler) Subscribe(c *cosnet.Context) any {
	var data subscriptionReq
	if err := c.Bind(&data); err != nil {
		return err
	}
	if c.Socket.Data() == nil {
		return nil
	}
	subs := h.getSubscriptions(c)
	for _, sub := range subs {
		if sub == data.Topic {
			return false
		}
	}
	h.setSubscriptions(c, append(subs, data.Topic))
	return true
}

func (h *serverHandler) Unsubscribe(c *cosnet.Context) any {
	var data unsubscriptionReq
	if err := c.Bind(&data); err != nil {
		return err
	}
	subs := h.getSubscriptions(c)
	if subs == nil {
		return nil
	}
	remove := make(map[string]bool, len(data.Topics))
	for _, t := range data.Topics {
		remove[t] = true
	}
	filtered := make([]string, 0, len(subs))
	for _, s := range subs {
		if !remove[s] {
			filtered = append(filtered, s)
		}
	}
	h.setSubscriptions(c, filtered)
	return nil
}

func (h *serverHandler) Publish(c *cosnet.Context) any {
	var msg publishReq
	if err := c.Bind(&msg); err != nil {
		return err
	}
	h.transport.onRemotePublish(msg.Topic, msg.Data, c.Socket)
	return nil
}

// clientHandler 客户端消息处理器
type clientHandler struct {
	transport *ClientTransport
}

func (h *clientHandler) Message(c *cosnet.Context) any {
	var msg publishReq
	if err := c.Bind(&msg); err != nil {
		return err
	}
	if h.transport.receiver != nil {
		h.transport.receiver(msg.Topic, msg.Data)
	}
	return nil
}
