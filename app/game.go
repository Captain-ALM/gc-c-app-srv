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
	//TODO: Game loop integration (Define struct before go routine, the return struct after go routine)
	return &Game{
		manager:     manager,
		connections: make(map[uuid.UUID]*Connection),
		hostConn:    hostConn,
		conRWMutex:  &sync.RWMutex{},
		quiz:        tQuiz,
		metadata:    gamMeta,
		endCallback: onEnd,
	}
}

type Game struct {
	manager     *db.Manager
	connections map[uuid.UUID]*Connection
	hostConn    *Connection
	conRWMutex  *sync.RWMutex
	quiz        tables.Quiz
	metadata    tables.Game
	endCallback func(game *Game)
}

func (g *Game) HasExpired() bool {
	if g == nil {
		return false
	}
	return g.metadata.Expiry.Before(time.Now())
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
	if g.IsActive() {
		g.metadata.State = byte(GameStateFinish)
		_ = g.manager.Delete(&g.metadata)
		g.conRWMutex.RLock()
		defer g.conRWMutex.RUnlock()
		for _, cc := range g.connections {
			cc.KickPlayer(false)
		}
		if g.endCallback != nil {
			g.endCallback(g)
		}
	}
	return nil
}

type GameState byte

const (
	GameStateLobby       = GameState(1)
	GameStateQuestion    = GameState(2)
	GameStateAnswerShow  = GameState(3)
	GameStateLeaderboard = GameState(4)
	GameStateFinish      = GameState(5)
)
