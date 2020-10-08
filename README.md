# websocket

High performance websocket server

## Example

```go
package main

import (
    "github.com/demskie/websocket"
    xwebsocket "golang.org/x/net/websocket"
)

manager, err := NewManager("127.0.0.1:12345", nil)
if err != nil {
    panic(err)
}

manager.SetReadPoolSize(1024)
manager.SetUpgradeTimeout(10 * time.Second)
manager.SetHandler(func(s *Session) {
    buf := &bytes.Buffer{}
    for i := 0; i < 123; i++ {
        _, err := s.Receive(buf, 0)
        if err != nil {
            panic(err)
        }
        s.Write(buf.Bytes())
    }
})

url := "ws://127.0.0.1:12345"
origin := "http://localhost"
ws, err := xwebsocket.Dial(url, "", origin)
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
```