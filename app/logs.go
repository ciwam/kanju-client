// Package app 内的日志中心：客户端与内嵌后端共用一个内存环形缓冲。
package app

import (
	"fmt"
	"io"
	"sync"
	"time"
)

const logCapacity = 500

// LogBuffer 进程内环形日志：最近 logCapacity 行。
type LogBuffer struct {
	mu   sync.Mutex
	pipe *io.PipeWriter
}

var (
	logLines     []string
	logListeners []chan struct{}
)

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
					idx := indexOfByte(buf, '\n')
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

var embeddedLogPipe *io.PipeWriter

func indexOfByte(b []byte, c byte) int {
	for i := range b {
		if b[i] == c {
			return i
		}
	}
	return -1
}

// pushLog 追加一行并通知监听者。
func pushLog(line string) {
	stamp := time.Now().Format("15:04:05")
	logLines = append(logLines, stamp+"  "+line)
	if len(logLines) > logCapacity {
		logLines = logLines[len(logLines)-logCapacity:]
	}
	for _, ch := range logListeners {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// ClientLog 客户端自己打日志（对外别名）。
func ClientLog(format string, args ...any) {
	pushLog(fmt.Sprintf(format, args...))
}

// BackendLogWriter 传给内嵌后端 ServeOptions.LogOutput。
func BackendLogWriter() io.Writer { return embeddedLogPipe }

// Logs 返回当前全部日志行。
func Logs() []string {
	out := make([]string, len(logLines))
	copy(out, logLines)
	return out
}

// ClearLogs 清空。
func ClearLogs() {
	logLines = nil
}

// WatchLogs 订阅新日志通知；返回取消函数。
func WatchLogs(ch chan struct{}) func() {
	logListeners = append(logListeners, ch)
	return func() {
		for i, c := range logListeners {
			if c == ch {
				logListeners = append(logListeners[:i], logListeners[i+1:]...)
				break
			}
		}
	}
}
