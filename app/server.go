package app

import (
	"crypto/rsa"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"golang.local/app-srv/conf"
	"golang.local/gc-c-com/packet"
	"golang.local/gc-c-com/packets"
	"golang.local/gc-c-com/transport"
	"golang.local/gc-c-db/db"
	"io"
	"log"
	"net/http"
	"os"
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
	connections map[transport.Transport]*Connection
	conRWMutex  *sync.RWMutex
	games       map[uint32]*Game
	gamRWMutex  *sync.RWMutex
	manager     *db.Manager
}

func (s *Server) Activate(config conf.ConfigYaml, pukk *rsa.PublicKey, router *mux.Router, manager *db.Manager) {
	if s == nil || s.active {
		return
	}
	s.active = true
	s.conRWMutex = &sync.RWMutex{}
	s.gamRWMutex = &sync.RWMutex{}
	s.manager = manager
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
	if s == nil || t == nil {
		return
	}
	conn := NewConnection(s.manager, t, time.Now().Add(s.config.App.GetConnectionLifetime()))
	if conn != nil {
		s.conRWMutex.Lock()
		defer s.conRWMutex.Unlock()
		s.connections[conn.GetID()] = conn
		go s.connectionMonitor(conn)
		go s.connectionProcessor(conn)
	}
}

func (s *Server) clientClose(t transport.Transport, err error) {
	if s == nil || t == nil {
		return
	}
	s.conRWMutex.Lock()
	defer s.conRWMutex.Unlock()
	delete(s.connections, t)
}

func (s *Server) Close() error {
	if s == nil || !s.active {
		return nil
	}
	s.active = false
	defer close(s.byeChan)
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

func (s *Server) connectionProcessor(conn *Connection) {
	defer func() { _ = conn.Close() }()
	for s.active && conn.IsActive() {
		select {
		case <-s.byeChan:
			return
		case <-conn.GetTerminationChannel():
			return
		case pk := <-conn.GetOuttakeServer():
			switch pk.GetCommand() {
			case packets.ID:
				if (s.publicKey != nil && pk.Valid(s.publicKey)) || InDebugMode() {
					var pyl packets.IDPayload
					err := pk.GetPayload(&pyl)
					if err != nil && pyl.ID == s.config.Identity.ID {
						InlineSend(conn, packet.FromNew(packets.NewID(s.config.Identity.ID, nil)))
						DebugPrintln("Master Connected")
						conn.Session = NewMasterSession()
					}
				}
			case packets.QueryStatus:
				if conn.Session.IsMasterServer() {
					InlineSend(conn, packet.FromNew(packets.NewCurrentStatus(s.config.Identity.ID, s.getConnectionCount(), uint32(s.config.App.GetMaxConnections()), nil)))
					DebugPrintln("Master Queried")
				}
			case packets.Halt:
				if conn.Session.IsMasterServer() {
					_ = s.Close()
					DebugPrintln("App Server Closed")
					return
				}
			case packets.AuthCheck:
				if conn.Session == nil {
					InlineSend(conn, packet.FromNew(packets.NewAuthStatus(packets.EnumAuthStatusLoggedOut, nil, "", nil)))
				} else {
					InlineSend(conn, packet.FromNew(packets.NewAuthStatus(packets.EnumAuthStatusLoggedIn, conn.Session.GetTokenHash(), conn.Session.GetEmail(), nil)))
				}
			case packets.AuthLogout:
				if conn.Session.Invalidate(s.manager) {
					conn.Session = nil
					InlineSend(conn, packet.FromNew(packets.NewAuthStatus(packets.EnumAuthStatusLoggedOut, nil, "", nil)))
				}
			case packets.UserDelete:
				if conn.Session != nil && conn.Session.DeleteUser(s.manager) {
					InlineSend(conn, packet.FromNew(packets.NewAuthStatus(packets.EnumAuthStatusLoggedOut, nil, "", nil)))
				}
			case packets.TokenLogin:
				var pyl packets.TokenLoginPayload
				err := pk.GetPayload(&pyl)
				if err != nil {
					if conn.Session == nil {
						conn.Session = NewSession(pyl.Token, nil, s.manager, s.config.App)
						if conn.Session == nil {
							InlineSend(conn, packet.FromNew(packets.NewAuthStatus(packets.EnumAuthStatusRejectedJWT, nil, "", nil)))
						} else {
							InlineSend(conn, packet.FromNew(packets.NewAuthStatus(packets.EnumAuthStatusAcceptedJWT, conn.Session.GetTokenHash(), conn.Session.GetEmail(), nil)))
							InlineSend(conn, packet.FromNew(packets.NewAuthStatus(packets.EnumAuthStatusLoggedIn, conn.Session.GetTokenHash(), conn.Session.GetEmail(), nil)))
						}
					} else {
						if conn.Session.ValidateJWT(pyl.Token, s.manager, s.config.App) {
							InlineSend(conn, packet.FromNew(packets.NewAuthStatus(packets.EnumAuthStatusAcceptedJWT, conn.Session.GetTokenHash(), conn.Session.GetEmail(), nil)))
						} else {
							InlineSend(conn, packet.FromNew(packets.NewAuthStatus(packets.EnumAuthStatusRejectedJWT, nil, "", nil)))
						}
					}
				}
			case packets.HashLogin:
				var pyl packets.HashLoginPayload
				err := pk.GetPayload(&pyl)
				if err != nil {
					if conn.Session == nil {
						conn.Session = NewSession("", pyl.Hash, s.manager, s.config.App)
						if conn.Session == nil {
							InlineSend(conn, packet.FromNew(packets.NewAuthStatus(packets.EnumAuthStatusRejectedHash, nil, "", nil)))
						} else {
							InlineSend(conn, packet.FromNew(packets.NewAuthStatus(packets.EnumAuthStatusAcceptedHash, conn.Session.GetTokenHash(), conn.Session.GetEmail(), nil)))
							InlineSend(conn, packet.FromNew(packets.NewAuthStatus(packets.EnumAuthStatusLoggedIn, conn.Session.GetTokenHash(), conn.Session.GetEmail(), nil)))
						}
					} else {
						if conn.Session.ValidateHash(pyl.Hash, s.manager) {
							InlineSend(conn, packet.FromNew(packets.NewAuthStatus(packets.EnumAuthStatusAcceptedHash, conn.Session.GetTokenHash(), conn.Session.GetEmail(), nil)))
						} else {
							InlineSend(conn, packet.FromNew(packets.NewAuthStatus(packets.EnumAuthStatusRejectedHash, nil, "", nil)))
						}
					}
				}
			}
		}
	}
}

func (s *Server) getConnectionCount() uint32 {
	if s == nil {
		return 0
	}
	s.gamRWMutex.RLock()
	defer s.gamRWMutex.RUnlock()
	return uint32(len(s.connections))
}

func (s *Server) gameEnd(game *Game) {
	if s == nil || game == nil {
		return
	}
	s.gamRWMutex.Lock()
	defer s.gamRWMutex.Unlock()
	delete(s.games, game.GetID())
}

func (s *Server) GetByeChannel() <-chan bool {
	if s == nil {
		return nil
	}
	return s.byeChan
}

func ForkedSend(conn *Connection, toSend *packet.Packet) {
	go InlineSend(conn, toSend)
}

func InlineSend(conn *Connection, toSend *packet.Packet) {
	select {
	case <-conn.GetTerminationChannel():
	case conn.GetIntake() <- toSend:
	}
}

func InDebugMode() bool {
	return os.Getenv("DEBUG") == "1"
}

func DebugPrintln(msg string) {
	if os.Getenv("DEBUG") == "1" {
		log.Println("DEBUG:", msg)
	}
}
