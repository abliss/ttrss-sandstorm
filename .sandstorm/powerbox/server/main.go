package main

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log"
	"net/http"
	"os"

	"github.com/gorilla/websocket"

	_ "github.com/go-sql-driver/mysql"
)

const webSocketListenAddr = ":3000"

var (
	proxyListenAddr = ":" + os.Getenv("POWERBOX_PROXY_PORT")

	mysqlUser = os.Getenv("MYSQL_USER")
	mysqlDb   = os.Getenv("MYSQL_DATABASE")
	mysqlUri  = mysqlUser + "@/" + mysqlDb
)

func chkfatal(err error) {
	if err != nil {
		panic(err)
	}
}

func main() {
	db, err := sql.Open("mysql", mysqlUri)
	chkfatal(err)
	storage, err := NewStorage(db)
	chkfatal(err)
	srv := NewServer(storage)

	go func() {
		panic(http.ListenAndServe(webSocketListenAddr, srv.newWebSocketServer()))
	}()
	panic(http.ListenAndServe(proxyListenAddr, srv.newProxyServer()))
}

func NewServer(storage Storage) Server {
	srv := Server{
		storage: storage,
		mu:      make(chan struct{}, 1),
	}
	srv.mu <- struct{}{}
	return srv
}

type Server struct {
	storage Storage

	// Protects fields below
	mu       chan struct{}
	wsConn   *websocket.Conn
	cancelWs context.CancelFunc
}

func (s Server) newProxyServer() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		url := req.URL.String()
		client, err := s.getClientFor(url)
		if err != nil {
			log.Printf("Failed to get client for %q: %v", url, err)
			w.WriteHeader(500)
			return
		}
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("Error making proxied request: %v", err)
			w.WriteHeader(500)
			return
		}
		defer resp.Body.Close()
		wh := w.Header()
		for k, v := range resp.Header {
			wh[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	})
}

func (s Server) getClientFor(url string) (*http.Client, error) {
	token, err := s.storage.GetTokenFor(url)
	if err != nil {
		return nil, err
	}
	_ = token
	return nil, errors.New("TODO")
}

func (s Server) newWebSocketServer() http.Handler {
	up := &websocket.Upgrader{}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := up.Upgrade(w, req, nil)
		if err != nil {
			log.Println(err)
			return
		}
		ctx, cancel := context.WithCancel(req.Context())
		s.newConn(conn, cancel)
		<-ctx.Done()
	})
}

func (s Server) newConn(conn *websocket.Conn, cancel context.CancelFunc) {
	s.withMu(func() {
		if s.wsConn != nil {
			s.wsConn.Close()
			s.cancelWs()
		}
		s.wsConn = conn
		s.cancelWs = cancel
	})
}

func (s Server) withMu(fn func()) {
	<-s.mu
	defer func() {
		s.mu <- struct{}{}
	}()
	fn()
}
