// Package app 内的日志中心：客户端与内嵌后端共用一个内存环形缓冲。
package app

import (
	"fmt"
	"io"
	"sync"
	"time"
)

const logCapacity = 500

var (
	logMu        sync.Mutex
	logLines     []string
	logListeners []chan struct{}
)

var embeddedLogPipe *io.PipeWriter

func init() {
	rd, wr := io.Pipe()
	// 后端 logger 只认 io.Writer；把 pipe 读端逐行收割进缓冲。
	go func() {
		buf := make([]byte, 0, 4096)
		tmp := make([]byte, 1024)
		for {
			n, err := rd.Read(tmp)
			if n > 0 {
				buf = append(buf, tmp[:n]...)
				for {
					idx := indexByte(buf, '\n')
					if idx < 0 {
						break
					}
					pushLog(string(buf[:idx]))
					buf = buf[idx+1:]
				}
				if len(buf) > 8192 { // 防御无换行的超长行
					pushLog(string(buf))
					buf = buf[:0]
				}
			}
			if err != nil {
				if len(buf) > 0 {
					pushLog(string(buf))
				}
				return
			}
		}
	}()
	embeddedLogPipe = wr
}

func indexByte(b []byte, c byte) int {
	for i := range b {
		if b[i] == c {
			return i
		}
	}
	return -1
}

// pushLog 追加一行并通知监听者。任意 goroutine 可调。
func pushLog(line string) {
	stamp := time.Now().Format("15:04:05")
	logMu.Lock()
	logLines = append(logLines, stamp+"  "+line)
	if len(logLines) > logCapacity {
		logLines = logLines[len(logLines)-logCapacity:]
	}
	listeners := make([]chan struct{}, len(logListeners))
	copy(listeners, logListeners)
	logMu.Unlock()
	for _, ch := range listeners {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// ClientLog 客户端自己打日志。
func ClientLog(format string, args ...any) {
	pushLog(fmt.Sprintf(format, args...))
}

// BackendLogWriter 传给内嵌后端 ServeOptions.LogOutput。
func BackendLogWriter() io.Writer { return embeddedLogPipe }

// Logs 返回当前全部日志行的快照。
func Logs() []string {
	logMu.Lock()
	defer logMu.Unlock()
	out := make([]string, len(logLines))
	copy(out, logLines)
	return out
}

// ClearLogs 清空。
func ClearLogs() {
	logMu.Lock()
	logLines = nil
	logMu.Unlock()
}

// WatchLogs 订阅新日志通知；返回取消函数。
func WatchLogs(ch chan struct{}) func() {
	logMu.Lock()
	logListeners = append(logListeners, ch)
	logMu.Unlock()
	return func() {
		logMu.Lock()
		for i, c := range logListeners {
			if c == ch {
				logListeners = append(logListeners[:i], logListeners[i+1:]...)
				break
			}
		}
		logMu.Unlock()
	}
}
