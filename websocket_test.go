package websocket

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	gwebsocket "github.com/gorilla/websocket"
	xwebsocket "golang.org/x/net/websocket"
)

func TestBasic(t *testing.T) {
	srvURL, srvCfg, cliURL, cliCfg := getTLSConfig()

	manager, err := NewManager(srvURL, srvCfg)
	if err != nil {
		panic(err)
	}

	manager.SetReadPoolSize(24)
	manager.SetReadPoolSize(65536)
	manager.SetUpgradeTimeout(time.Hour)

	manager.SetHandler(func(s *Session) {
		buf := &bytes.Buffer{}
		for i := 0; i < 123; i++ {
			_, err := s.Receive(buf, 5)
			if err != nil {
				panic(err)
			}
			s.Write(buf.Bytes())
		}
	})

	executeClientRequests(manager, cliURL, cliCfg, 1024)
}

func TestParallel(t *testing.T) {
	srvURL, srvCfg, cliURL, cliCfg := getTLSConfig()

	manager, err := NewManager(srvURL, srvCfg)
	if err != nil {
		panic(err)
	}

	manager.SetReadPoolSize(24)
	manager.SetReadPoolSize(65536)
	manager.SetUpgradeTimeout(time.Hour)

	manager.SetHandler(func(s *Session) {
		buf := &bytes.Buffer{}
		for i := 0; i < 123; i++ {
			_, err := s.Receive(buf, -1)
			if err != nil {
				panic(err)
			}
			s.Write(buf.Bytes())
		}
	})

	width := 512

	wg := sync.WaitGroup{}
	wg.Add(width)

	for i := 0; i < width; i++ {
		go func(n int) {
			executeClientRequests(manager, cliURL, cliCfg, 512)
			wg.Done()
		}(i)
	}

	wg.Wait()
}

func TestBasicAlternate(t *testing.T) {
	manager, err := NewManager("127.0.0.1:12345", nil)
	if err != nil {
		panic(err)
	}

	manager.SetReadPoolSize(24)
	manager.SetReadPoolSize(65536)
	manager.SetUpgradeTimeout(time.Hour)

	manager.SetHandler(func(s *Session) {
		buf := &bytes.Buffer{}
		for i := 0; i < 123; i++ {
			_, err := s.Receive(buf, 512)
			if err != nil {
				panic(err)
			}
			s.Write(buf.Bytes())
		}
	})

	ws, err := xwebsocket.Dial("ws://127.0.0.1:12345", "", "http://localhost")
	if err != nil {
		panic(err)
	}

	n, err := ws.Write([]byte("hello, world!\n"))
	if err != nil {
		panic(err)
	}

	payload := make([]byte, n)
	_, err = ws.Read(payload)
	if err != nil {
		panic(err)
	}

	if bytes.Compare(payload, []byte("hello, world!\n")) != 0 {
		panic(string(payload))
	}
}

func executeClientRequests(manager *Manager, url string, cfg *tls.Config, iterations int) {
	c := getTestClientConn(url, cfg)
	defer c.Close()
	for i := 0; i < iterations; i++ {
		err := c.WriteMessage(gwebsocket.BinaryMessage, []byte(strconv.Itoa(i)))
		if err != nil {
			c.Close()
			panic(err)
		}
		_, p, err := c.ReadMessage()
		if err != nil {
			c.Close()
			panic(err)
		}
		x, err := strconv.Atoi(string(p))
		if err != nil {
			panic(err)
		} else if i != x {
			log.Fatalf("should have recieved %d and not %d", i, x)
		}
		if i == iterations/2 {
			manager.SetReadPoolSize(2)
		}
	}
}

func getTLSConfig() (string, *tls.Config, string, *tls.Config) {
	server := httptest.NewTLSServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {},
	))
	defer server.Close()
	certs := x509.NewCertPool()
	for _, c := range server.TLS.Certificates {
		roots, err := x509.ParseCertificates(c.Certificate[len(c.Certificate)-1])
		if err != nil {
			panic(err)
		}
		for _, root := range roots {
			certs.AddCert(root)
		}
	}
	serverAddr := strings.TrimPrefix(server.URL, "https://")
	serverConfig := server.TLS
	clientURL := "wss" + strings.TrimPrefix(server.URL, "https")
	clientConfig := &tls.Config{RootCAs: certs}
	return serverAddr, serverConfig, clientURL, clientConfig
}

func getTestClientConn(url string, tlsConfig *tls.Config) (c *gwebsocket.Conn) {
	dialer := &gwebsocket.Dialer{
		ReadBufferSize:   1024,
		WriteBufferSize:  1024,
		HandshakeTimeout: 1 * time.Second,
		Subprotocols:     []string{"foo", "bar"},
		TLSClientConfig:  tlsConfig,
	}
	var err error
	for j := 0; j < 65536; j++ {
		c, _, err = dialer.Dial(url, nil)
		if err == nil {
			break
		}
		time.Sleep(time.Microsecond)
	}
	if err != nil {
		panic(err)
	}
	return c
}
