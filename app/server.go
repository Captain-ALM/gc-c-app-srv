package app

import (
	"crypto/rsa"
	"encoding/json"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"golang.local/app-srv/conf"
	"golang.local/gc-c-com/packet"
	"golang.local/gc-c-com/packets"
	"golang.local/gc-c-com/transport"
	"golang.local/gc-c-db/db"
	"golang.local/gc-c-db/tables"
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
	s.byeChan = make(chan bool)
	s.connections = make(map[transport.Transport]*Connection)
	s.games = make(map[uint32]*Game)
	if s.publicKey == nil {
		s.GetPublicKey()
	}
	wsListener := &transport.ListenWebsocket{Upgrader: websocket.Upgrader{HandshakeTimeout: config.App.GetTimeout(), ReadBufferSize: 8192, WriteBufferSize: 8192}}
	rsListener := &transport.ListenHandler{}
	s.multiListen = transport.NewMultiListener([]transport.Listener{wsListener, rsListener}, nil, s.clientConnect, s.clientClose, config.App.GetTimeout())
	DebugPrintln("Timeout:" + config.App.GetTimeout().String())
	wsListener.Activate()
	rsListener.Activate()
	for _, cd := range config.Listen.Domains {
		DebugPrintln(cd + config.Listen.GetBasePrefixURL() + config.Identity.GetID())
		router.Host(cd).Path(config.Listen.GetBasePrefixURL() + config.Identity.GetID() + "/ws").Handler(wsListener)
		router.Host(cd).Path(config.Listen.GetBasePrefixURL() + config.Identity.GetID() + "/rs").Handler(rsListener)
		router.Host(cd).Path(config.Listen.GetBasePrefixURL() + config.Identity.GetID() + "/").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
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
	DebugPrintln("Client Connected: " + t.GetID() + " : " + t.GetTimeout().String())
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
	DebugPrintln("Client Disconnected: " + t.GetID())
	DebugErrIsNil(err)
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
		DebugPrintln(err.Error())
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
			DebugPrintln("PK_CMD: " + pk.GetCommand())
			switch pk.GetCommand() {
			case packets.ID:
				DebugErrIsNil(pk.Verify(s.publicKey))
				if (s.publicKey != nil && pk.Valid(s.publicKey)) || os.Getenv("NO_MASTER_VERIFY") == "1" {
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
			case packets.NewGame:
				if conn.GetGameID() == 0 {
					var pyl packets.NewGamePayload
					err := pk.GetPayload(&pyl)
					if err != nil && pyl.QuizID > 0 && pyl.MaxCountdown > 0 {
						if s.newGame(&pyl, conn) {
							InlineSend(conn, packet.FromNew(packets.NewIDGuest(conn.GetPlayerID(), nil)))
						} else {
							InlineSend(conn, packet.FromNew(packets.NewGameError("Failed To Host", nil)))
						}
					}
				}
			case packets.JoinGame:
				if conn.GetGameID() == 0 {
					var pyl packets.JoinGamePayload
					err := pk.GetPayload(&pyl)
					if err != nil && pyl.ID > 0 {
						ok := conn.JoinGame(pyl.ID, pyl.Nickname)
						tGame := s.getGameInstance(pyl.ID)
						if ok && tGame != nil {
							ok = tGame.AddGuest(conn)
							if ok {
								InlineSend(conn, packet.FromNew(packets.NewIDGuest(conn.GetPlayerID(), nil)))
								InlineSend(conn, packet.FromNew(packets.NewGameStatus("Joining...", nil)))
							} else {
								InlineSend(conn, packet.FromNew(packets.NewGameError("Failed To Join", nil)))
							}
						} else {
							InlineSend(conn, packet.FromNew(packets.NewGameNotFound(nil)))
						}
					}
				}
			case packets.IDGuest:
				if conn.GetGameID() == 0 {
					var pyl packets.IDPayload
					err := pk.GetPayload(&pyl)
					if err != nil && pyl.ID > 0 {
						ok, isTheHost := conn.RejoinGame(pyl.ID)
						tGame := s.getGameInstance(pyl.ID)
						if ok && tGame != nil {
							if isTheHost {
								ok = tGame.ReAddHost(conn)
							} else {
								ok = tGame.AddGuest(conn)
							}
							if !ok {
								conn.KickPlayer(false)
								InlineSend(conn, packet.FromNew(packets.NewGameError("Failed To Rejoin", nil)))
								InlineSend(conn, packet.FromNew(packets.NewIDGuest(0, nil)))
							}
						} else {
							InlineSend(conn, packet.FromNew(packets.NewGameNotFound(nil)))
							InlineSend(conn, packet.FromNew(packets.NewIDGuest(0, nil)))
						}
					}
				}
			case packets.QuizRequest:
				if conn.Session == nil {
					InlineSend(conn, packet.FromNew(packets.NewAuthStatus(packets.EnumAuthStatusRequired, nil, "", nil)))
				} else {
					var pyl packets.IDPayload
					err := pk.GetPayload(&pyl)
					if err != nil {
						if s.isQuizAccessible(pyl.ID, conn.Session, false) {
							tQuiz := s.loadQuiz(pyl.ID)
							if tQuiz == nil {
								InlineSend(conn, packet.FromNew(packets.NewQuizState(pyl.ID, packets.EnumQuizStateNotFound, nil)))
							} else {
								tQs := s.loadQuestions(tQuiz)
								tAs := s.loadAnswers(tQuiz)
								if tQs == nil || tAs == nil {
									InlineSend(conn, packet.FromNew(packets.NewQuizState(pyl.ID, packets.EnumQuizStateNotFound, nil)))
								} else {
									InlineSend(conn, packet.FromNew(packets.NewQuizData(pyl.ID, tQuiz.Name, *tQs, *tAs, nil)))
								}
							}
						} else {
							InlineSend(conn, packet.FromNew(packets.NewQuizState(pyl.ID, packets.EnumQuizStateNotFound, nil)))
						}
					}
				}
			case packets.QuizSearch:
				if conn.Session == nil {
					InlineSend(conn, packet.FromNew(packets.NewAuthStatus(packets.EnumAuthStatusRequired, nil, "", nil)))
				} else {
					var pyl packets.QuizSearchPayload
					err := pk.GetPayload(&pyl)
					if err != nil {
						sRes := s.searchQuizzes(pyl.Name, pyl.Filter, conn.Session)
						var qLE []packets.QuizListEntry
						for _, csr := range sRes {
							qLE = append(qLE, packets.QuizListEntry{ID: csr.ID, Name: csr.Name, Mine: conn.Session.GetEmail() == csr.OwnerEmail, Public: csr.IsPublic})
						}
						InlineSend(conn, packet.FromNew(packets.NewQuizList(qLE, nil)))
					}
				}
			case packets.QuizDelete:
				if conn.Session == nil {
					InlineSend(conn, packet.FromNew(packets.NewAuthStatus(packets.EnumAuthStatusRequired, nil, "", nil)))
				} else {
					var pyl packets.IDPayload
					err := pk.GetPayload(&pyl)
					if err != nil {
						if s.isQuizAccessible(pyl.ID, conn.Session, true) {
							err := s.manager.Delete(&tables.Quiz{ID: pyl.ID})
							if DebugErrIsNil(err) {
								InlineSend(conn, packet.FromNew(packets.NewQuizState(pyl.ID, packets.EnumQuizStateDeleted, nil)))
							}
						} else {
							InlineSend(conn, packet.FromNew(packets.NewQuizState(pyl.ID, packets.EnumQuizStateNotFound, nil)))
						}
					}
				}
			case packets.QuizUpload:
				if conn.Session == nil {
					InlineSend(conn, packet.FromNew(packets.NewAuthStatus(packets.EnumAuthStatusRequired, nil, "", nil)))
				} else {
					var pyl packets.QuizDataPayload
					err := pk.GetPayload(&pyl)
					if err != nil {
						if pyl.ID == 0 || !s.isQuizAccessible(pyl.ID, conn.Session, true) {
							if pyl.ID == 0 {
								tQuiz := &tables.Quiz{
									ID:         pyl.ID,
									OwnerEmail: conn.Session.GetEmail(),
									Name:       pyl.Name,
									IsPublic:   false,
								}
								s.saveQuestions(tQuiz, &pyl.Questions)
								s.saveAnswers(tQuiz, &pyl.Answers)
								if s.saveQuiz(tQuiz) {
									InlineSend(conn, packet.FromNew(packets.NewQuizState(pyl.ID, packets.EnumQuizStateCreated, nil)))
								} else {
									InlineSend(conn, packet.FromNew(packets.NewQuizState(pyl.ID, packets.EnumQuizStateUploadFailed, nil)))
								}
							} else {
								InlineSend(conn, packet.FromNew(packets.NewQuizState(pyl.ID, packets.EnumQuizStateNotFound, nil)))
							}
						} else {
							tQuiz := s.loadQuizMetadata(pyl.ID)
							if tQuiz == nil {
								InlineSend(conn, packet.FromNew(packets.NewQuizState(pyl.ID, packets.EnumQuizStateUploadFailed, nil)))
							} else {
								s.saveQuestions(tQuiz, &pyl.Questions)
								s.saveAnswers(tQuiz, &pyl.Answers)
								if s.saveQuiz(tQuiz) {
									InlineSend(conn, packet.FromNew(packets.NewQuizState(pyl.ID, packets.EnumQuizStateCreated, nil)))
								} else {
									InlineSend(conn, packet.FromNew(packets.NewQuizState(pyl.ID, packets.EnumQuizStateUploadFailed, nil)))
								}
							}
						}
					}
				}
			case packets.QuizVisibility:
				if conn.Session == nil {
					InlineSend(conn, packet.FromNew(packets.NewAuthStatus(packets.EnumAuthStatusRequired, nil, "", nil)))
				} else {
					var pyl packets.QuizVisibilityPayload
					err := pk.GetPayload(&pyl)
					if err != nil {
						if s.isQuizAccessible(pyl.ID, conn.Session, true) {
							tQuiz := s.loadQuizMetadata(pyl.ID)
							if tQuiz != nil {
								tQuiz.IsPublic = pyl.Public
								if s.saveQuizMetadata(tQuiz) {
									if pyl.Public {
										InlineSend(conn, packet.FromNew(packets.NewQuizState(pyl.ID, packets.EnumQuizStatePublic, nil)))
									} else {
										InlineSend(conn, packet.FromNew(packets.NewQuizState(pyl.ID, packets.EnumQuizStatePrivate, nil)))
									}
								}
							}
						} else {
							InlineSend(conn, packet.FromNew(packets.NewQuizState(pyl.ID, packets.EnumQuizStateNotFound, nil)))
						}
					}
				}
			}
		}
	}
}

func (s *Server) newGame(sGamePayload *packets.NewGamePayload, hostConn *Connection) bool {
	if s == nil || sGamePayload == nil || hostConn == nil {
		return false
	}
	tQuiz := s.loadQuiz(sGamePayload.QuizID)
	if tQuiz == nil {
		return false
	}
	tQs := s.loadQuestions(tQuiz)
	tAs := s.loadAnswers(tQuiz)
	qDPyl := &packets.QuizDataPayload{ID: tQuiz.ID, Name: tQuiz.Name, Questions: *tQs, Answers: *tAs}
	tGame := NewGame(s.manager, s.gameEnd, hostConn, qDPyl, s.config.Identity.ID, sGamePayload.MaxCountdown, sGamePayload.StreakEnabled, time.Now().Add(s.config.App.GetGameLifetime()))
	if tGame == nil {
		return false
	}
	s.gamRWMutex.Lock()
	defer s.gamRWMutex.Unlock()
	s.games[tGame.GetID()] = tGame
	go s.gameMonitor(tGame)
	return true
}

func (s *Server) getGameInstance(gameID uint32) *Game {
	if s == nil || gameID == 0 {
		return nil
	}
	s.gamRWMutex.RLock()
	defer s.gamRWMutex.RUnlock()
	tGame, ok := s.games[gameID]
	if ok {
		return tGame
	}
	return nil
}

func (s *Server) getConnectionCount() uint32 {
	if s == nil {
		return 0
	}
	s.conRWMutex.RLock()
	defer s.conRWMutex.RUnlock()
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

func (s *Server) searchQuizzes(sTerm string, sFilter packets.EnumQuizSearchFilter, cSession *Session) []tables.Quiz {
	if s == nil || s.manager == nil || s.manager.Engine == nil || cSession == nil {
		return nil
	}
	var toRet []tables.Quiz
	var wSQL []string
	var pSQL []interface{}
	switch sFilter {
	case packets.EnumQuizSearchFilterAll:
		wSQL = append(wSQL, "(Owner_Email = ? OR Quiz_Public = ?)")
		pSQL = append(pSQL, cSession.GetEmail(), true)
	case packets.EnumQuizSearchFilterMine:
		wSQL = append(wSQL, "(Owner_Email = ?)")
		pSQL = append(pSQL, cSession.GetEmail())
	case packets.EnumQuizSearchFilterMyPrivate:
		wSQL = append(wSQL, "(Owner_Email = ? AND Quiz_Public = ?)")
		pSQL = append(pSQL, cSession.GetEmail(), false)
	case packets.EnumQuizSearchFilterMyPublic:
		wSQL = append(wSQL, "(Owner_Email = ? AND Quiz_Public = ?)")
		pSQL = append(pSQL, cSession.GetEmail(), true)
	case packets.EnumQuizSearchFilterOtherUsers:
		wSQL = append(wSQL, "(Owner_Email != ? AND Quiz_Public = ?)")
		pSQL = append(pSQL, cSession.GetEmail(), true)
	default:
		return nil
	}
	if sTerm != "" {
		wSQL = append(wSQL, "AND Quiz_Name LIKE ?")
		pSQL = append(pSQL, "%"+sTerm+"%")
	}
	dbSess := s.manager.Engine.UseBool().Cols("Quiz_ID", "Owner_Email", "Quiz_Name", "Quiz_Public").Where(strings.Join(wSQL, " "), pSQL...)
	err := dbSess.Find(&toRet)
	if err != nil {
		DebugPrintln(err.Error())
		return nil
	}
	return toRet
}

func (s *Server) getGameIDGivenGuestID(guestID uint32) uint32 {
	if s == nil {
		return 0
	}
	tGuest := tables.Guest{ID: guestID}
	err := s.manager.Load(&tGuest)
	if err != nil {
		DebugPrintln(err.Error())
		return 0
	}
	return tGuest.GameID
}

func (s *Server) isQuizAccessible(quizID uint32, cSession *Session, owned bool) bool {
	if s == nil || cSession == nil || quizID == 0 {
		return false
	}
	tQuiz := s.loadQuizMetadata(quizID)
	if tQuiz == nil {
		return false
	}
	return tQuiz.OwnerEmail == cSession.GetEmail() || (!owned && tQuiz.IsPublic)
}

func (s *Server) loadQuizMetadata(quizID uint32) *tables.Quiz {
	if s == nil || quizID == 0 || s.manager == nil || s.manager.Engine == nil {
		return nil
	}
	tQuiz := tables.Quiz{ID: quizID}
	exists, err := s.manager.Engine.UseBool().Cols("Quiz_ID", "Owner_Email", "Quiz_Name", "Quiz_Public").Get(&tQuiz)
	if !exists || err != nil {
		if err != nil {
			DebugPrintln(err.Error())
		}
		return nil
	}
	return &tQuiz
}

func (s *Server) saveQuizMetadata(quiz *tables.Quiz) bool {
	if s == nil || quiz == nil || s.manager == nil || s.manager.Engine == nil {
		return false
	}
	_, err := s.manager.Engine.UseBool().Update(quiz, quiz.GetIDObject())
	return DebugErrIsNil(err)
}

func (s *Server) loadQuiz(quizID uint32) *tables.Quiz {
	if s == nil || quizID == 0 {
		return nil
	}
	tQuiz := tables.Quiz{ID: quizID}
	err := s.manager.Load(&tQuiz)
	if err != nil {
		DebugPrintln(err.Error())
		return nil
	}
	return &tQuiz
}

func (s *Server) saveQuiz(quiz *tables.Quiz) bool {
	if s == nil || quiz == nil {
		return false
	}
	err := s.manager.Save(quiz)
	return DebugErrIsNil(err)
}

func (s *Server) loadQuestions(quizEntry *tables.Quiz) *packets.QuizQuestions {
	if s == nil || quizEntry == nil {
		return nil
	}
	var tQs packets.QuizQuestions
	err := json.Unmarshal(quizEntry.Questions, &tQs)
	if err != nil {
		return nil
	}
	return &tQs
}

func (s *Server) loadAnswers(quizEntry *tables.Quiz) *packets.QuizAnswers {
	if s == nil || quizEntry == nil {
		return nil
	}
	var tAs packets.QuizAnswers
	err := json.Unmarshal(quizEntry.Answers, &tAs)
	if err != nil {
		return nil
	}
	return &tAs
}

func (s *Server) saveQuestions(quizEntry *tables.Quiz, quizQs *packets.QuizQuestions) bool {
	if s == nil || quizEntry == nil || quizQs == nil {
		return false
	}
	bts, err := json.Marshal(quizQs)
	if err != nil {
		return false
	}
	quizEntry.Questions = bts
	return true
}

func (s *Server) saveAnswers(quizEntry *tables.Quiz, quizAs *packets.QuizAnswers) bool {
	if s == nil || quizEntry == nil || quizAs == nil {
		return false
	}
	bts, err := json.Marshal(quizAs)
	if err != nil {
		return false
	}
	quizEntry.Answers = bts
	return true
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

func DebugPrintln(msg string) {
	if os.Getenv("DEBUG") == "1" {
		log.Println("DEBUG:", msg)
	}
}

func DebugErrIsNil(err error) bool {
	if err == nil {
		return true
	}
	DebugPrintln(err.Error())
	return false
}
