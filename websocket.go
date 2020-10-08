package websocket

import (
	"bytes"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"reflect"
	"sync"
	"time"
	"unsafe"

	"github.com/gammazero/workerpool"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/mailru/easygo/netpoll"
)

type Manager struct {
	mutex   *sync.RWMutex
	up      ws.Upgrader
	td      time.Duration
	fn      func(*Session)
	wp      *workerpool.WorkerPool
	wpMutex *sync.Mutex
}

func NewManager(address string, tlsCfg *tls.Config) (*Manager, error) {
	poller, fe := netpoll.New(nil)
	if fe != nil {
		panic(fe)
	}
	ln, fe := net.Listen("tcp", address)
	if fe != nil {
		panic(fe)
	}
	acceptDesc := netpoll.Must(netpoll.HandleListener(
		ln, netpoll.EventRead|netpoll.EventOneShot,
	))
	m := &Manager{
		mutex:   &sync.RWMutex{},
		up:      ws.DefaultUpgrader,
		td:      5 * time.Second,
		fn:      func(*Session) {},
		wp:      workerpool.New(65536),
		wpMutex: &sync.Mutex{},
	}
	fe = poller.Start(acceptDesc, func(e netpoll.Event) {
		m.pool().Submit(func() {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			if tlsCfg != nil {
				conn = tls.Server(conn, tlsCfg)
			}
			hdr, proto, uri, host, err := upgradeConnection(conn, m.upgrader(), m.timeout())
			if err != nil {
				conn.Close()
				return
			}
			executing := false
			s := createSession(tlsCfg != nil, conn, poller, hdr, proto, uri, host)
			fe := poller.Start(s.readDesc, func(ev netpoll.Event) {
				if ev&(netpoll.EventReadHup|netpoll.EventHup) != 0 {
					poller.Stop(s.readDesc)
				} else {
					s.rxChan <- struct{}{}
					if !executing {
						executing = true
						executeFn := func() {}
						executeFn = func() {
							m.pool().Submit(func() {
								m.handlerFunc()(s)
								if len(s.rxChan) > 0 {
									executeFn()
								} else {
									executing = false
								}
							})
						}
						executeFn()
					}
				}
			})
			if fe != nil {
				panic(fe)
			}
		})
		fe := poller.Resume(acceptDesc)
		if fe != nil {
			panic(fe)
		}
	})
	if fe != nil {
		panic(fe)
	}
	return m, nil
}

func (m *Manager) SetHandler(handler func(s *Session)) {
	m.mutex.Lock()
	m.fn = handler
	m.mutex.Unlock()
}

func (m *Manager) SetUpgradeTimeout(timeout time.Duration) {
	m.pool().Submit(func() {
		m.mutex.Lock()
		m.td = timeout
		m.mutex.Unlock()
	})
}

func (m *Manager) SetReadPoolSize(size int) {
	defer m.wpMutex.Unlock()
	m.wpMutex.Lock()
	if size < 2 {
		size = 2
	}
	oldPool := m.wp
	newPool := workerpool.New(size)
	signalChan := make(chan struct{}, size)
	for i := 0; i < size; i++ {
		newPool.Submit(func() {
			<-signalChan
		})
		oldPool.Submit(func() {
			signalChan <- struct{}{}
		})
	}
}

func (m *Manager) upgrader() ws.Upgrader {
	defer m.mutex.RUnlock()
	m.mutex.RLock()
	return m.up
}

func (m *Manager) timeout() time.Duration {
	defer m.mutex.RUnlock()
	m.mutex.RLock()
	return m.td
}

func (m *Manager) handlerFunc() func(s *Session) {
	defer m.mutex.RUnlock()
	m.mutex.RLock()
	return m.fn
}

func (m *Manager) pool() *workerpool.WorkerPool {
	defer m.mutex.RUnlock()
	m.mutex.RLock()
	return m.wp
}

type Session struct {
	conn             net.Conn
	poller           netpoll.Poller
	readDesc         *netpoll.Desc
	rxChan           chan struct{}
	header           http.Header
	proto, uri, host string
	localAddr        net.Addr
	remoteAddr       net.Addr
	onCloseFunc      func()
	onCloseMutex     sync.Mutex
	onCloseOnce      sync.Once
}

func (s *Session) Receive(buf *bytes.Buffer, byteLimit int64) ([]byte, error) {
	_, okay := <-s.rxChan
	if !okay {
		return nil, errors.New("session has been closed")
	}
	bytes, err := readClientDataWithBuffer(s.conn, buf, byteLimit)
	if err != nil && err != io.EOF {
		s.Close()
		return nil, err
	}
	err = s.poller.Resume(s.readDesc)
	if err != nil {
		s.Close()
		return nil, err
	}
	return bytes, nil
}

func (s *Session) ReceiveWithTimeout(t time.Duration, buf *bytes.Buffer, byteLimit int64) ([]byte, error) {
	select {
	case _, okay := <-s.rxChan:
		if !okay {
			return nil, errors.New("session has been closed")
		}
		bytes, err := readClientDataWithBuffer(s.conn, buf, byteLimit)
		if err != nil && err != io.EOF {
			s.Close()
			return nil, err
		}
		err = s.poller.Resume(s.readDesc)
		if err != nil {
			s.Close()
			return nil, err
		}
		return bytes, nil
	case <-time.NewTicker(t).C:
	}
	err := s.poller.Resume(s.readDesc)
	if err != nil {
		s.Close()
		return nil, err
	}
	return nil, errors.New("receive method timed out")
}

func (s *Session) Write(data []byte) error {
	err := wsutil.WriteServerBinary(s.conn, data)
	if err != nil {
		s.Close()
	}
	return err
}

func (s *Session) OnClose(callback func()) {
	s.onCloseMutex.Lock()
	s.onCloseFunc = callback
	s.onCloseMutex.Unlock()
}

func (s *Session) Close() {
	s.onCloseOnce.Do(func() {
		s.poller.Stop(s.readDesc)
		s.conn.Close()
		s.onCloseMutex.Lock()
		if s.onCloseFunc != nil {
			s.onCloseFunc()
		}
		s.onCloseMutex.Unlock()
	})
}

func (s *Session) Subprotocol() string {
	return s.proto
}

func (s *Session) URI() string {
	return s.uri
}

func (s *Session) Host() string {
	return s.host
}

func (s *Session) LocalAddr() net.Addr {
	return s.localAddr
}

func (s *Session) RemoteAddr() net.Addr {
	return s.remoteAddr
}

func (s *Session) Headers() http.Header {
	return s.header
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func upgradeConnection(conn net.Conn, up ws.Upgrader, timeout time.Duration) (http.Header, string, string, string, error) {
	var (
		hdr   = http.Header{}
		proto string
		uri   string
		host  string
	)
	upgrader := ws.Upgrader{
		Protocol: func(b []byte) bool {
			proto = string(b)
			if up.Protocol != nil {
				return up.Protocol(b)
			}
			return true
		},
		OnRequest: func(b []byte) error {
			uri = string(b)
			if up.OnRequest != nil {
				return up.OnRequest(b)
			}
			return nil
		},
		OnHost: func(b []byte) error {
			host = string(b)
			if up.OnRequest != nil {
				return up.OnHost(b)
			}
			return nil
		},
		OnHeader: func(key, value []byte) error {
			hdr.Add(string(key), string(value))
			if up.OnHeader != nil {
				return up.OnHeader(key, value)
			}
			return nil
		},
	}
	_, err := upgrader.Upgrade(deadlinedConn{conn, timeout})
	return hdr, proto, uri, host, err
}

func createSession(isTLS bool, conn net.Conn, poller netpoll.Poller, hdr http.Header, proto, uri, host string) *Session {
	fdConn := conn
	if isTLS {
		// https://github.com/golang/go/issues/29257
		tlsConn, _ := conn.(*tls.Conn)
		val := reflect.ValueOf(tlsConn).Elem().FieldByName("conn")
		val = reflect.NewAt(val.Type(), unsafe.Pointer(val.UnsafeAddr())).Elem()
		fdConn = val.Interface().(net.Conn)
	}
	return &Session{
		conn:       conn,
		poller:     poller,
		rxChan:     make(chan struct{}, 1),
		readDesc:   netpoll.Must(netpoll.HandleReadOnce(fdConn)),
		header:     hdr,
		proto:      proto,
		uri:        uri,
		host:       host,
		localAddr:  conn.LocalAddr(),
		remoteAddr: conn.RemoteAddr(),
	}
}

func readClientDataWithBuffer(rw net.Conn, buf *bytes.Buffer, byteLimit int64) ([]byte, error) {
	controlHandler := wsutil.ControlFrameHandler(rw, ws.StateServerSide)
	rd := wsutil.Reader{
		Source:          rw,
		State:           ws.StateServerSide,
		CheckUTF8:       true,
		SkipHeaderCheck: false,
		OnIntermediate:  controlHandler,
	}
	for {
		hdr, err := rd.NextFrame()
		if err != nil {
			return nil, err
		}
		if hdr.OpCode.IsControl() {
			err := controlHandler(hdr, &rd)
			if err != nil {
				return nil, err
			}
			continue
		}
		if hdr.OpCode&(ws.OpText|ws.OpBinary) == 0 {
			err := rd.Discard()
			if err != nil {
				return nil, err
			}
			continue
		}
		if buf == nil {
			buf = &bytes.Buffer{}
		}
		buf.Reset()
		if byteLimit > 0 {
			_, err = buf.ReadFrom(io.LimitReader(&rd, byteLimit))
			if byteLimit < hdr.Length {
				err = io.ErrUnexpectedEOF
			}
			return buf.Bytes(), err
		}
		_, err = buf.ReadFrom(&rd)
		return buf.Bytes(), err
	}
}

type deadlinedConn struct {
	net.Conn
	t time.Duration
}

func (d deadlinedConn) Write(p []byte) (int, error) {
	if err := d.Conn.SetWriteDeadline(time.Now().Add(d.t)); err != nil {
		return 0, err
	}
	return d.Conn.Write(p)
}

func (d deadlinedConn) Read(p []byte) (int, error) {
	if err := d.Conn.SetReadDeadline(time.Now().Add(d.t)); err != nil {
		return 0, err
	}
	return d.Conn.Read(p)
}
