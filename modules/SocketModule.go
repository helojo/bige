package modules

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"sync/atomic"
	"time"

	"github.com/buguang01/bige/messages"
	"github.com/buguang01/util/threads"

	"github.com/buguang01/Logger"
)

type SocketModule struct {
	ipPort          string                           //HTTP监听的地址
	timeout         time.Duration                    //超时时间
	RouteHandle     messages.IMessageHandle          //消息路由
	socketOnlineFun func(conn *messages.SocketModel) //连接成功后回调，可以用来获取一些连接的信息，比如IP
	getnum          int64                            //收到的总消息数
	runing          int64                            //当前在处理的消息数
	connlen         int64                            //连接数
	netList         net.Listener                     //监听对象
	thgo            *threads.ThreadGo                //协程管理器
}

//Init 初始化
func (mod *SocketModule) Init() {
	var err error
	mod.netList, err = net.Listen("tcp", mod.ipPort)
	if err != nil {
		panic(err)
	}
}

//Start 启动
func (mod *SocketModule) Start() {
	mod.thgo.Go(func(ctx context.Context) {
		Logger.PStatus("Socket Module Start!")
		for {
			conn, err := mod.netList.Accept()
			if err != nil {
				Logger.PStatus("Socket run Server closed under requeset!!")
				return
			}
			mod.thgo.Go(func(ctx context.Context) {
				mod.handle(conn)
			})
		}
	})

}

//Stop 停止
func (mod *SocketModule) Stop() {
	if err := mod.netList.Close(); err != nil {
		Logger.PError(err, "Close Socket Module:")
	}
	mod.thgo.CloseWait()
	Logger.PStatus("Socket Module Stop.")
}

//PrintStatus 打印状态
func (mod *SocketModule) PrintStatus() string {
	return fmt.Sprintf(
		"\r\n\t\tsocket Module\t:%d/%d/%d\t(connum/getmsg/runing)",
		atomic.LoadInt64(&mod.connlen),
		atomic.LoadInt64(&mod.getnum),
		atomic.LoadInt64(&mod.runing))
}

func (mod *SocketModule) handle(conn net.Conn) {
	defer conn.Close()

	//发给下面的连接对象，可以自定义一些信息和回调
	skmd := new(messages.SocketModel)
	skmd.Conn = conn
	skmd.KeyID = -1
	if mod.socketOnlineFun != nil {
		mod.socketOnlineFun(skmd)
	}
	atomic.AddInt64(&mod.connlen, 1)
	//发消息来说明这个用户掉线了
	defer func() {
		atomic.AddInt64(&mod.connlen, -1)
		Logger.PDebugKey("socket client closeing:%+v .", skmd.KeyID, skmd.ConInfo)
		//用来处理发生连接关闭的时候，要处理的事
		if skmd.CloseFun != nil {
			skmd.CloseFun(skmd)
		}
		Logger.PDebugKey("socket client close:%+v .", skmd.KeyID, skmd.ConInfo)
	}()
	Logger.PDebugKey("socket client open:%+v .", skmd.KeyID, skmd.ConInfo)
	runchan := make(chan bool, 8) //用来处理超时
	mod.thgo.Go(
		func(ctx context.Context) {
			timeout := time.NewTimer(mod.timeout)
			defer timeout.Stop()
			defer conn.Close()
			for {
				select {
				case <-ctx.Done():
					return
				case <-timeout.C:
					return
				case ok := <-runchan:
					if ok {
						timeout.Reset(mod.timeout)
					} else {
						return
					}
				}
			}
			//超时关连接
		})
	mod.thgo.Try(
		func(ctx context.Context) {
			buf := &bytes.Buffer{}
		listen:
			for {
				buff, err := ioutil.ReadAll(conn)
				if err != nil {
					if err == io.EOF {
						runchan <- false
					}
					break listen
				}
				buf.Write(buff)
				buff = buf.Bytes()
				msglen, ok := mod.RouteHandle.CheckMaxLenVaild(buff)
				if !ok {
					if msglen == 0 {
						//消息长度异常
						break listen
					}
					continue
				}

				msg, err := mod.RouteHandle.Unmarshal(buff[:msglen])
				if err != nil {
					Logger.PInfo("RouteHandle Unmarshal Error:%s", err.Error())
					return
				}
				modmsg, ok := msg.(messages.ISocketMessageHandle)
				if !ok {
					Logger.PInfo("Not is socket Msg:%+v", msg)
					return
				} else {
					Logger.PInfo("socket Get Msg:%+v", msg)
				}
				buf.Reset()
				if uint32(len(buff)) > msglen {
					buf.Write(buff[msglen:])
				}
				runchan <- true
				atomic.AddInt64(&mod.getnum, 1)
				mod.thgo.Try(func(ctx context.Context) {
					atomic.AddInt64(&mod.runing, 1)
					modmsg.SocketDirectCall(skmd)
				}, nil, func() {
					atomic.AddInt64(&mod.runing, -1)
				})

			}
		},
		nil,
		nil,
	)
}