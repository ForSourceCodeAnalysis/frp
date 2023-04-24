package main

import (
	"os"

	"frp/models/server"
	"frp/utils/conn"
	"frp/utils/log"
)

func main() {
	/**
	 * 加载服务端配置文件，配置项主要有
	 * 监听的地址，端口
	 * 日志配置
	 * 与客户端对应的穿透配置
	 */
	err := server.LoadConf("./frps.ini")
	if err != nil {
		os.Exit(-1)
	}

	log.InitLog(server.LogWay, server.LogFile, server.LogLevel)

	//创建监听服务，这个服务主要监听的是frp client的连接，而不是用户的连接
	l, err := conn.Listen(server.BindAddr, server.BindPort)
	if err != nil {
		log.Error("Create listener error, %v", err)
		os.Exit(-1)
	}

	log.Info("Start frps success")
	//处理监听到的连接
	ProcessControlConn(l)
}
