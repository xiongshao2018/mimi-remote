package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestAppServerGatewayFrameWriteWindowScalesWithPayload(t *testing.T) {
	tests := []struct {
		name         string
		payloadBytes int
		want         time.Duration
	}{
		{name: "empty", payloadBytes: 0, want: 10 * time.Second},
		{name: "small frame boundary", payloadBytes: 256 << 10, want: 10 * time.Second},
		{name: "first large frame byte", payloadBytes: (256 << 10) + 1, want: 11 * time.Second},
		{name: "four MiB history page", payloadBytes: 4 << 20, want: 50 * time.Second},
		{name: "five MiB history page", payloadBytes: 5 << 20, want: 61 * time.Second},
		{name: "hard upper bound", payloadBytes: 64 << 20, want: 75 * time.Second},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := appServerGatewayFrameWriteWindow(test.payloadBytes); got != test.want {
				t.Fatalf("write window = %s, want %s", got, test.want)
			}
		})
	}
}

func TestAppServerGatewayPongWindowCoversDelayedPingDuringLargeFrameWrite(t *testing.T) {
	minimum := appServerGatewayPingPeriod +
		appServerGatewayLargeFrameMaxWriteWindow + appServerGatewayWriteWindow
	if margin := appServerGatewayPongWait - minimum; margin < appServerGatewayPongGrace {
		t.Fatalf("pong wait margin = %s, want at least %s", margin, appServerGatewayPongGrace)
	}
}

func TestAppServerGatewayClientPingRenewsReadDeadline(t *testing.T) {
	// iOS 16 的 URLSessionWebSocketTask 不会自动应答服务端 ping，只能主动发
	// 客户端 ping。网关必须把客户端 ping 视为活性（续期 + 回 pong），否则
	// 空闲的旧客户端连接必被 pongWait 踢线。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		configureGatewayReadConn(conn)
		// 覆盖初始 150s deadline：ping 若续期，读会重新阻塞到 pongWait；
		// 若不续期，读会在 300ms 后超时返回。
		_ = conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	wsURL := "ws://" + strings.TrimPrefix(server.URL, "http://")
	client, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	// gorilla 的 pongHandler 只在 ReadMessage 读循环里被调用，必须先起读 goroutine。
	pongs := make(chan string, 1)
	client.SetPongHandler(func(data string) error {
		pongs <- data
		return nil
	})
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		for {
			if _, _, err := client.ReadMessage(); err != nil {
				return
			}
		}
	}()

	if err := client.WriteControl(websocket.PingMessage, []byte("keepalive"), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	select {
	case data := <-pongs:
		if data != "keepalive" {
			t.Fatalf("pong payload = %q, want %q", data, "keepalive")
		}
	case <-closed:
		t.Fatal("connection closed before answering client ping")
	case <-time.After(2 * time.Second):
		t.Fatal("gateway did not answer client ping")
	}

	// 续期判据：pong 之后服务端读 deadline 已推到 now+150s，连接在远超
	// 300ms 初始 deadline 的窗口内保持存活；未续期时服务端读会在 300ms
	// 超时并关闭连接，客户端读 goroutine 随即退出。
	select {
	case <-closed:
		t.Fatal("connection closed before keep-alive window elapsed")
	case <-time.After(1200 * time.Millisecond):
	}
}

func TestAppServerGatewayControlWriteDeadlineStartsAfterLockWait(t *testing.T) {
	upstreamURL, _, _ := fakeAppServerUpstream(t, nil)
	conn, _, err := websocket.DefaultDialer.Dial(upstreamURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	var writeMu sync.Mutex
	writeMu.Lock()
	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		close(started)
		result <- writeWebSocketControl(conn, &writeMu, websocket.PingMessage, nil, 10*time.Millisecond)
	}()
	<-started
	// 模拟 ping 在一个慢速大帧之后排队，等待时间故意超过自身 write window。
	time.Sleep(30 * time.Millisecond)
	writeMu.Unlock()

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("control write failed after lock wait: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("control write remained blocked after lock release")
	}
}
