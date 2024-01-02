package app

import (
	"golang.local/gc-c-db/db"
	"golang.local/gc-c-db/tables"
	"strings"
)

func getNick(nick string) string {
	for len(nick) < 64 {
		nick += " "
	}
	return nick[:64]
}

func isNickHost(nick string) bool {
	if len(nick) < 32 {
		return true
	}
	for i := 0; i < 32; i++ {
		if byte(nick[i]) != 0 {
			return false
		}
	}
	return true
}

func NewPlayer(id uint32, nick string, gameID uint32, manager *db.Manager) *Player {
	plyMeta := tables.Guest{ID: id}
	var err error
	if id > 0 {
		err = manager.Load(&plyMeta)
	} else {
		plyMeta.Name = getNick(nick)
		plyMeta.GameID = gameID
		err = manager.Save(&plyMeta)
	}
	if err == nil {
		return &Player{metadata: plyMeta, host: isNickHost(plyMeta.Name)}
	}
	return nil
}

type Player struct {
	streakMultiplier uint32
	answered         bool
	host             bool
	metadata         tables.Guest
}

func (p *Player) AddScore(amount uint32, correct bool, manager *db.Manager) {
	if p == nil || p.host || p.answered {
		return
	}
	p.answered = true
	p.metadata.Score += amount * (p.streakMultiplier + 1)
	if correct {
		p.streakMultiplier += 1
	}
	_ = manager.Save(&p.metadata)
}

func (p *Player) GetNickname() string {
	if p == nil {
		return ""
	}
	if p.host {
		return p.metadata.Name
	}
	return strings.TrimRight(p.metadata.Name, " ")
}

func (p *Player) ResetAnswered() {
	if p == nil {
		return
	}
	p.answered = false
}

func (p *Player) IsHost() bool {
	if p == nil {
		return false
	}
	return p.host
}

func (p *Player) DeleteGuest(manager *db.Manager) bool {
	if p == nil {
		return false
	}
	err := manager.Delete(&p.metadata)
	return err == nil
}
