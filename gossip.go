package pubsub

import (
	"github.com/hwcer/cosnet"
)

// gossipServerList 执行Gossip协议，交换服务器列表
func (ps *PubSub) gossipServerList() {
	// 使用IsServer方法判断是否启动了服务器
	isServer := ps.IsServer()
	serverInfo := ServerInfo{
		Address:      ps.Address(),
		IsServer:     isServer,
		OtherServers: ps.getKnownServersList(),
	}

	// 所有节点（包括客户端）都需要执行Gossip协议来发现服务器
	// 但客户端节点只发送自己的信息，不转发其他服务器的信息
	ps.Range(func(socket *cosnet.Socket) bool {
		socket.Send(0, 0, PathHandshake, HandshakeData{
			ServerInfo: serverInfo,
		})
		return true
	})
}

// connectToKnownServers 尝试连接已知的服务器
func (ps *PubSub) connectToKnownServers() {
	servers := ps.getKnownServersList()
	localAddress := ps.Address()

	// 所有节点（包括客户端）都需要尝试连接到已知的服务器
	for _, addr := range servers {
		// 跳过本地地址
		if addr == localAddress {
			continue
		}

		// 检查是否已经存在到该服务器地址的连接
		alreadyConnected := false
		ps.Range(func(socket *cosnet.Socket) bool {
			if data := socket.Data(); data != nil {
				if connInfo, ok := data.Get("connection_info").(ConnectionInfo); ok {
					if connInfo.Address == addr {
						alreadyConnected = true
						return false
					}
				}
			}
			return true
		})

		// 如果没有连接，则尝试连接
		if !alreadyConnected {
			go func(address string) {
				_, err := ps.Connect(address)
				if err != nil {
					// 连接失败，稍后会重试
				}
			}(addr)
		}
	}
}

// getKnownServersList 获取已知服务器列表
func (ps *PubSub) getKnownServersList() []string {
	servers := make([]string, 0)
	localAddress := ps.Address()

	// 通过socket.Data中的ConnectionInfo获取已知服务器列表
	ps.Range(func(socket *cosnet.Socket) bool {
		if data := socket.Data(); data != nil {
			if connInfo, ok := data.Get("connection_info").(ConnectionInfo); ok {
				if connInfo.IsServer && connInfo.Address != "" && connInfo.Address != localAddress {
					servers = append(servers, connInfo.Address)
				}
			}
		}
		return true
	})

	return servers
}

// sendHandshake 发送握手信息
func (ps *PubSub) sendHandshake(socket *cosnet.Socket) {
	// 使用IsServer方法判断是否启动了服务器
	isServer := ps.IsServer()
	serverInfo := ServerInfo{
		Address:      ps.Address(),
		IsServer:     isServer,
		OtherServers: ps.getKnownServersList(),
	}

	socket.Send(0, 0, PathHandshake, HandshakeData{
		ServerInfo: serverInfo,
	})
}

// handleHandshake 处理握手信息
func (ps *PubSub) handleHandshake(socket *cosnet.Socket, data HandshakeData) {
	// 使用IsServer方法判断是否启动了服务器
	isServer := ps.IsServer()

	remoteServerInfo := data.ServerInfo

	// 保存对方信息到socket.Data中的ConnectionInfo
	socketData := socket.Data()
	var connInfo ConnectionInfo
	if info, ok := socketData.Get("connection_info").(ConnectionInfo); ok {
		connInfo = info
	} else {
		connInfo = ConnectionInfo{
			Subscriptions:      []string{},
			QueueSubscriptions: make(map[string]string),
		}
	}
	connInfo.Address = remoteServerInfo.Address
	connInfo.IsServer = remoteServerInfo.IsServer
	socketData.Set("connection_info", connInfo)

	// 如果对方是服务器，且我们也是服务器，则回复我们的服务器信息
	if remoteServerInfo.IsServer && isServer {
		ps.sendHandshake(socket)
	}
}
