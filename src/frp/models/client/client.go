package client

import (
	"encoding/json"

	"frp/models/consts"
	"frp/models/msg"
	"frp/utils/conn"
	"frp/utils/log"
)

type ProxyClient struct {
	Name      string
	Passwd    string
	LocalPort int64
}

func (p *ProxyClient) GetLocalConn() (c *conn.Conn, err error) {
	c, err = conn.ConnectServer("127.0.0.1", p.LocalPort)
	if err != nil {
		log.Error("ProxyName [%s], connect to local port error, %v", p.Name, err)
	}
	return
}

func (p *ProxyClient) GetRemoteConn(addr string, port int64) (c *conn.Conn, err error) {
	defer func() {
		if err != nil {
			c.Close()
		}
	}()
	//这次连接是为了建立隧道
	//frpc除了启动时会主动和frps连接外，还会在收到frps转发的用户请求信息时再次和frps连接
	c, err = conn.ConnectServer(addr, port)
	if err != nil {
		log.Error("ProxyName [%s], connect to server [%s:%d] error, %v", p.Name, addr, port, err)
		return
	}

	req := &msg.ClientCtlReq{
		Type:      consts.WorkConn, //这次的消息类型是WorkConn，frps收到这个类型的消息会在用户连接，frps与frpc连接之间建立隧道
		ProxyName: p.Name,
		Passwd:    p.Passwd,
	}

	buf, _ := json.Marshal(req)
	err = c.Write(string(buf) + "\n")
	if err != nil {
		log.Error("ProxyName [%s], write to server error, %v", p.Name, err)
		return
	}

	err = nil
	return
}

// 创建隧道，本地连接和远程连接之间
func (p *ProxyClient) StartTunnel(serverAddr string, serverPort int64) (err error) {
	localConn, err := p.GetLocalConn()
	if err != nil {
		return
	}
	remoteConn, err := p.GetRemoteConn(serverAddr, serverPort)
	if err != nil {
		return
	}

	// l means local, r means remote
	log.Debug("Join two conns, (l[%s] r[%s]) (l[%s] r[%s])", localConn.GetLocalAddr(), localConn.GetRemoteAddr(),
		remoteConn.GetLocalAddr(), remoteConn.GetRemoteAddr())
	go conn.Join(localConn, remoteConn)
	return nil
}
