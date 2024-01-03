package app

import (
	"github.com/google/uuid"
	"golang.local/gc-c-db/db"
	"golang.local/gc-c-db/tables"
	"sync"
	"time"
)

func NewGame(manager *db.Manager, onEnd func(game *Game), hostConn *Connection, quizID uint32, serverID uint32, countdownMax uint32, streakEnabled bool, expiry time.Time) *Game {
	if manager == nil || hostConn == nil {
		return nil
	}
	gamMeta := tables.Game{
		QuizID:        quizID,
		ServerID:      serverID,
		CountdownMax:  countdownMax,
		StreakEnabled: streakEnabled,
		Expiry:        expiry,
		State:         byte(GameStateLobby),
	}
	err := manager.Save(&gamMeta)
	if err != nil {
		return nil
	}
	tQuiz, err := (&gamMeta).GetParentQuiz(manager.Engine)
	if err != nil {
		return nil
	}
	conns := make(map[uuid.UUID]*Connection)
	conns[hostConn.GetID()] = hostConn
	nGam := &Game{
		manager:       manager,
		connections:   conns,
		hostConn:      hostConn,
		hostConnNotif: make(chan bool),
		conRWMutex:    &sync.RWMutex{},
		quiz:          tQuiz,
		metadata:      gamMeta,
		endCallback:   onEnd,
		closeMutex:    &sync.Mutex{},
		termChan:      make(chan bool),
	}
	go nGam.gameSendLoop()
	go nGam.hostRecvLoop(hostConn)
	return nGam
}

type Game struct {
	manager       *db.Manager
	connections   map[uuid.UUID]*Connection
	hostConn      *Connection
	hostConnNotif chan bool
	conRWMutex    *sync.RWMutex
	quiz          tables.Quiz
	metadata      tables.Game
	endCallback   func(game *Game)
	closeMutex    *sync.Mutex
	termChan      chan bool
}

func (g *Game) AddGuest(newGuest *Connection) bool {
	if g == nil || newGuest == nil {
		return false
	}
	g.conRWMutex.Lock()
	defer g.conRWMutex.Unlock()
	if _, has := g.connections[newGuest.GetID()]; has {
		return false
	}
	g.connections[newGuest.GetID()] = newGuest
	go g.guestRecvLoop(newGuest)
	return true
}

func (g *Game) ReAddHost(newHost *Connection) bool {
	if g == nil || newHost == nil {
		return false
	}
	g.conRWMutex.Lock()
	defer g.conRWMutex.Unlock()
	if newHost == g.hostConn || (g.hostConn != nil && g.hostConn.IsActive()) {
		return false
	}
	if g.hostConn != nil {
		delete(g.connections, g.hostConn.GetID())
	}
	g.hostConn = newHost
	g.connections[newHost.GetID()] = newHost
	go g.hostRecvLoop(newHost)
	g.hostConnNotif <- true
	return true
}

func (g *Game) RemoveConnection(conn *Connection) bool {
	if g == nil || conn == nil {
		return false
	}
	g.conRWMutex.Lock()
	defer g.conRWMutex.Unlock()
	if conn == g.hostConn {
		g.hostConnNotif <- false
		g.hostConn = nil
		delete(g.connections, conn.GetID())
	}
	delete(g.connections, conn.GetID())
	return true
}

func (g *Game) gameSendLoop() {
	//TODO: Finish
}

func (g *Game) hostRecvLoop(conn *Connection) {
	defer g.RemoveConnection(conn)
	//TODO: Finish
}

func (g *Game) guestRecvLoop(conn *Connection) {
	defer g.RemoveConnection(conn)
	//TODO: Finish
}

func (g *Game) HasExpired() bool {
	if g == nil {
		return false
	}
	return g.metadata.Expiry.Before(time.Now())
}

func (g *Game) TimeTillExpiry() time.Duration {
	if g == nil {
		return 0
	}
	return time.Until(g.metadata.Expiry)
}

func (g *Game) GetTerminationChannel() <-chan bool {
	if g == nil {
		return nil
	}
	return g.termChan
}

func (g *Game) IsActive() bool {
	if g == nil {
		return false
	}
	return g.metadata.State > 0 && g.metadata.State < byte(GameStateFinish)
}

func (g *Game) Close() error {
	if g == nil {
		return nil
	}
	g.closeMutex.Lock()
	defer g.closeMutex.Unlock()
	if g.IsActive() {
		g.metadata.State = byte(GameStateFinish)
		_ = g.manager.Delete(&g.metadata)
		close(g.hostConnNotif)
		close(g.termChan)
		g.kickAll()
		if g.endCallback != nil {
			g.endCallback(g)
		}
	}
	return nil
}

func (g *Game) kickAll() {
	g.conRWMutex.RLock()
	defer g.conRWMutex.RUnlock()
	for _, cc := range g.connections {
		cc.KickPlayer(false)
	}
}

type GameState byte

const (
	GameStateLobby       = GameState(1)
	GameStateQuestion    = GameState(2)
	GameStateAnswerShow  = GameState(3)
	GameStateLeaderboard = GameState(4)
	GameStateFinish      = GameState(5)
)
