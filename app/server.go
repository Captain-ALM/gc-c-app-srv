package app

import (
	"crypto/rsa"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"golang.local/app-srv/conf"
	"golang.local/gc-c-com/transport"
	"golang.local/gc-c-db/db"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Server struct {
	active      bool
	multiListen *transport.MultiListener
	publicKey   *rsa.PublicKey
	config      conf.ConfigYaml
	byeChan     chan bool
	connections map[uuid.UUID]*Connection
	conRWMutex  *sync.RWMutex
	games       map[uint32]*Game
	gamRWMutex  *sync.RWMutex
}

func (s *Server) Activate(config conf.ConfigYaml, pukk *rsa.PublicKey, router *mux.Router, manager *db.Manager) {
	if s == nil || s.active {
		return
	}
	s.active = true
	s.conRWMutex = &sync.RWMutex{}
	s.gamRWMutex = &sync.RWMutex{}
	s.config = config
	s.publicKey = pukk
	if s.publicKey == nil {
		s.GetPublicKey()
	}
	wsListener := &transport.ListenWebsocket{Upgrader: websocket.Upgrader{HandshakeTimeout: config.Listen.GetReadTimeout(), ReadBufferSize: 8192, WriteBufferSize: 8192}}
	rsListener := &transport.ListenHandler{}
	s.multiListen = transport.NewMultiListener([]transport.Listener{wsListener, rsListener}, nil, s.clientConnect, s.clientClose, config.Listen.GetReadTimeout())
	wsListener.Activate()
	rsListener.Activate()
	for _, cd := range config.Listen.Domains {
		router.Host(cd).Path(config.Listen.GetBasePrefixURL() + config.Identity.GetID() + "/ws").Handler(wsListener)
		router.Host(cd).Path(config.Listen.GetBasePrefixURL() + config.Identity.GetID() + "/rs").Handler(rsListener)
	}
}

func (s *Server) connectionMonitor(conn *Connection) {
	tOut := time.NewTimer(conn.TimeTillExpiry())
	defer tOut.Stop()
	select {
	case <-s.byeChan:
	case <-tOut.C:
		_ = conn.Close()
	case <-conn.GetTerminationChannel():
	}
}

func (s *Server) gameMonitor(game *Game) {
	tOut := time.NewTimer(game.TimeTillExpiry())
	defer tOut.Stop()
	select {
	case <-s.byeChan:
	case <-tOut.C:
		_ = game.Close()
	case <-game.GetTerminationChannel():
	}
}

func (s *Server) clientConnect(l transport.Listener, t transport.Transport) {
	if s == nil {
		return
	}
	//TODO: Implement via method in connection passing the server pointer too as well as app config
	panic("not implemented")
}

func (s *Server) clientClose(t transport.Transport, err error) {
	if s == nil {
		return
	}
	//TODO: Implement via method in connection passing the server pointer too as well as app config
	panic("not implemented")
}

func (s *Server) Close() error {
	if s == nil || !s.active {
		return nil
	}
	s.active = false
	close(s.byeChan)
	return s.multiListen.Close()
}

func (s *Server) GetPublicKey() {
	rsp, err := http.Get(s.config.Identity.GetPublicKeyURL())
	if err != nil {
		return
	}
	defer func() { _, _ = io.Copy(io.Discard, rsp.Body); _ = rsp.Body.Close() }()
	if rsp.StatusCode == http.StatusOK && rsp.ContentLength > 0 && strings.EqualFold(rsp.Header.Get("Content-Type"), "application/x-pem-file") {
		kbts := make([]byte, rsp.ContentLength)
		_, err := io.ReadAtLeast(rsp.Body, kbts, int(rsp.ContentLength))
		if err == nil {
			pubk, err := jwt.ParseRSAPublicKeyFromPEM(kbts)
			if err == nil {
				s.publicKey = pubk
			}
		}
	}
}

func (s *Server) connectionProcessor(conn *Connection, cnf conf.AppYaml) {
	//TODO: Finish
}
