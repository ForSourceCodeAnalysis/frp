package main

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"frp/models/consts"
	"frp/models/msg"
	"frp/models/server"
	"frp/utils/conn"
	"frp/utils/log"
)

/**
 * 处理frpc的连接
 * frpc除了在第一次启动时会发起连接，在收到frps转发的用户消息时也会发起连接
 * 只是两次连接发送的消息类型不同，第一次是consts.CtlConn，第二次是consts.WorkConn
 * 收到CtlConn类型消息，frps会根据配置信息创建代理服务
 * 收到WorkConn类型消息，frps会在用户连接和本次连接（即frpc新发起的连接）间创建隧道以便转发用户和frpc信息
 */

func ProcessControlConn(l *conn.Listener) {
	// 循环处理frpct的连接
	for {
		//通过通道实现的，如果没有连接过来，会一直阻塞
		c, err := l.GetConn()
		if err != nil {
			return
		}
		log.Debug("Get one new conn, %v", c.GetRemoteAddr())
		//接收到一个frpc连接后，创建一个协程处理后续事宜
		go controlWorker(c)
	}
}

// connection from every client and server
func controlWorker(c *conn.Conn) {
	// the first message is from client to server
	// if error, close connection
	res, err := c.ReadLine()
	if err != nil {
		log.Warn("Read error, %v", err)
		return
	}
	log.Debug("get: %s", res)

	clientCtlReq := &msg.ClientCtlReq{}
	clientCtlRes := &msg.ClientCtlRes{}
	if err := json.Unmarshal([]byte(res), &clientCtlReq); err != nil {
		log.Warn("Parse err: %v : %s", err, res)
		return
	}

	// check
	succ, info, needRes := checkProxy(clientCtlReq, c)
	if !succ {
		clientCtlRes.Code = 1
		clientCtlRes.Msg = info
	}

	if needRes { //CtlConn类型消息需要回复
		defer c.Close()

		buf, _ := json.Marshal(clientCtlRes)
		err = c.Write(string(buf) + "\n")
		if err != nil {
			log.Warn("Write error, %v", err)
			time.Sleep(1 * time.Second)
			return
		}
	} else { //注意，WorkConn类型的消息，走到这里就会返回了
		// work conn, just return
		return
	}
	//下面处理的是CtlConn或心跳类型消息，一般只会在frpc第一次启动连接时处理一次
	// other messages is from server to client
	s, ok := server.ProxyServers[clientCtlReq.ProxyName]
	if !ok {
		log.Warn("ProxyName [%s] is not exist", clientCtlReq.ProxyName)
		return
	}

	//处理心跳协程
	// read control msg from client
	go readControlMsgFromClient(s, c)

	serverCtlReq := &msg.ClientCtlReq{}
	serverCtlReq.Type = consts.WorkConn
	//主循环，等待用户连接
	for {
		//这个是怎么触发的呢？还记得上面的checkProxy函数里面创建的代理服务吗
		//用户连接的其实是代理服务，代理服务接收到用户连接后，会直接把连接压入队列，然后通过
		//通道触发这里
		closeFlag := s.WaitUserConn()
		if closeFlag {
			log.Debug("ProxyName [%s], goroutine for dealing user conn is closed", s.Name)
			break
		}
		buf, _ := json.Marshal(serverCtlReq)
		//接收到用户连接后，会向frpc连接发送一条类型为WorkConn的消息，frpc收到消息后会向frps发起一个新的连接，
		//并同样发送一条类型为WorkConn的消息，然后新连接和内网服务之间建立一个隧道，而frps收到WorkConn消息后
		//会在用户连接和新连接之间建立隧道，这样一条user<->frps_proxy<->(frps<->frpc)<->local_server
		//的网路就建立起来了
		err = c.Write(string(buf) + "\n")
		if err != nil {
			log.Warn("ProxyName [%s], write to client error, proxy exit", s.Name)
			s.Close()
			return
		}

		log.Debug("ProxyName [%s], write to client to add work conn success", s.Name)
	}

	log.Info("ProxyName [%s], I'm dead!", s.Name)
	return
}

// 检查代理服务，根据消息类型做出不同的响应
// 1. 如果是CtlConn（此消息类型需要回复），就检查代理服务是否已经创建，如果没有，根据配置信息创建代理服务
// 2. 如果是WorkConn（此消息类型不需要回复），就获取当前连接以触发隧道的建立
func checkProxy(req *msg.ClientCtlReq, c *conn.Conn) (succ bool, info string, needRes bool) {
	succ = false
	needRes = true
	// check if proxy name exist
	s, ok := server.ProxyServers[req.ProxyName]
	if !ok {
		info = fmt.Sprintf("ProxyName [%s] is not exist", req.ProxyName)
		log.Warn(info)
		return
	}

	// check password
	if req.Passwd != s.Passwd {
		info = fmt.Sprintf("ProxyName [%s], password is not correct", req.ProxyName)
		log.Warn(info)
		return
	}

	// control conn
	if req.Type == consts.CtlConn {
		if s.Status != consts.Idle { //frp client第一次连接到frps后，会紧接着请求一条创建代理服务的消息
			info = fmt.Sprintf("ProxyName [%s], already in use", req.ProxyName)
			log.Warn(info)
			return
		}

		// start proxy and listen for user conn, no block
		err := s.Start()
		if err != nil {
			info = fmt.Sprintf("ProxyName [%s], start proxy error: %v", req.ProxyName, err.Error())
			log.Warn(info)
			return
		}

		log.Info("ProxyName [%s], start proxy success", req.ProxyName)
	} else if req.Type == consts.WorkConn {
		// work conn
		needRes = false
		if s.Status != consts.Working {
			log.Warn("ProxyName [%s], is not working when it gets one new work conn", req.ProxyName)
			return
		}

		s.GetNewCliConn(c)
	} else {
		info = fmt.Sprintf("ProxyName [%s], type [%d] unsupport", req.ProxyName, req.Type)
		log.Warn(info)
		return
	}

	succ = true
	return
}

func readControlMsgFromClient(s *server.ProxyServer, c *conn.Conn) {
	isContinueRead := true
	f := func() {
		isContinueRead = false
		s.Close()
		log.Error("ProxyName [%s], client heartbeat timeout", s.Name)
	}
	timer := time.AfterFunc(time.Duration(server.HeartBeatTimeout)*time.Second, f)
	defer timer.Stop()

	for isContinueRead {
		content, err := c.ReadLine()
		if err != nil {
			if err == io.EOF {
				log.Warn("ProxyName [%s], client is dead!", s.Name)
				s.Close()
				break
			} else if nil == c || c.IsClosed() {
				log.Warn("ProxyName [%s], client connection is closed", s.Name)
				break
			}

			log.Error("ProxyName [%s], read error: %v", s.Name, err)
			continue
		}

		clientCtlReq := &msg.ClientCtlReq{}
		if err := json.Unmarshal([]byte(content), clientCtlReq); err != nil {
			log.Warn("Parse err: %v : %s", err, content)
			continue
		}
		if consts.CSHeartBeatReq == clientCtlReq.Type {
			log.Debug("ProxyName [%s], get heartbeat", s.Name)
			timer.Reset(time.Duration(server.HeartBeatTimeout) * time.Second)

			clientCtlRes := &msg.ClientCtlRes{}
			clientCtlRes.GeneralRes.Code = consts.SCHeartBeatRes
			response, err := json.Marshal(clientCtlRes)
			if err != nil {
				log.Warn("Serialize ClientCtlRes err! err: %v", err)
				continue
			}

			err = c.Write(string(response) + "\n")
			if err != nil {
				log.Error("Send heartbeat response to client failed! Err:%v", err)
				continue
			}
		}
	}
}
