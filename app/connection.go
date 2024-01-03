package app

import (
	"crypto/sha256"
	"github.com/google/uuid"
	"golang.local/gc-c-com/packet"
	"golang.local/gc-c-com/transport"
	"golang.local/gc-c-db/db"
	"sync"
	"time"
)

func NewConnection(manager *db.Manager, transport transport.Transport, expiry time.Time) *Connection {
	nConn := &Connection{
		manager:       manager,
		id:            uuid.New(),
		transport:     transport,
		intake:        make(chan *packet.Packet),
		outtakeServer: make(chan *packet.Packet),
		outtakeGame:   make(chan *packet.Packet),
		Session:       nil,
		plaMutex:      &sync.Mutex{},
		player:        nil,
		expires:       expiry,
		closeMutex:    &sync.Mutex{},
		termChan:      make(chan bool),
	}
	go nConn.intakePump()
	go nConn.outtakePump()
	return nConn
}

type Connection struct {
	manager       *db.Manager
	id            uuid.UUID
	transport     transport.Transport
	intake        chan *packet.Packet
	outtakeServer chan *packet.Packet
	outtakeGame   chan *packet.Packet
	Session       *Session
	plaMutex      *sync.Mutex
	player        *Player
	expires       time.Time
	closeMutex    *sync.Mutex
	termChan      chan bool
}

func (c *Connection) GetID() uuid.UUID {
	if c == nil {
		return uuid.Nil
	}
	return c.id
}

func (c *Connection) GetIntake() chan<- *packet.Packet {
	if c == nil {
		return nil
	}
	return c.intake
}

func (c *Connection) intakePump() {
	for c.transport.IsActive() {
		pk, ok := <-c.intake
		if ok {
			_ = c.transport.Send(pk)
		} else {
			return
		}
	}
}

func (c *Connection) GetOuttakeServer() <-chan *packet.Packet {
	if c == nil {
		return nil
	}
	return c.outtakeServer
}

func (c *Connection) GetOuttakeGame() <-chan *packet.Packet {
	if c == nil {
		return nil
	}
	return c.outtakeGame
}

func (c *Connection) outtakePump() {
	cWG := &sync.WaitGroup{}
	for c.transport.IsActive() {
		cWG.Add(2)
		pk, err := c.transport.Receive()
		if err == nil {
			go func() { c.outtakeServer <- pk; cWG.Done() }()
			go func() { c.outtakeGame <- pk; cWG.Done() }()
			cWG.Wait()
		} else {
			return
		}
	}
}

func (c *Connection) GetTerminationChannel() <-chan bool {
	if c == nil {
		return nil
	}
	return c.termChan
}

func (c *Connection) getHostNickname() string {
	if c == nil || c.Session == nil {
		return ""
	}
	nick := make([]byte, 32)
	s256Email := sha256.Sum256([]byte(c.Session.metadata.Email))
	return string(append(nick, s256Email[:]...))
}

func (c *Connection) HostGame(gameID uint32) bool {
	if c == nil {
		return false
	}
	c.plaMutex.Lock()
	defer c.plaMutex.Unlock()
	if c.player != nil {
		c.player.DeleteGuest(c.manager)
	}
	c.player = NewPlayer(0, c.getHostNickname(), gameID, c.manager)
	return c.player != nil
}

func (c *Connection) JoinGame(gameID uint32, nick string) bool {
	if c == nil {
		return false
	}
	c.plaMutex.Lock()
	defer c.plaMutex.Unlock()
	if c.player != nil {
		return false
	}
	c.player = NewPlayer(0, nick, gameID, c.manager)
	return c.player != nil
}

func (c *Connection) RejoinGame(guestID uint32) bool {
	if c == nil || guestID == 0 {
		return false
	}
	c.plaMutex.Lock()
	defer c.plaMutex.Unlock()
	if c.player != nil {
		return false
	}
	c.player = NewPlayer(guestID, "", 0, c.manager)
	return c.player != nil
}

func (c *Connection) AddScore(score uint32, correct bool) {
	if c == nil {
		return
	}
	c.plaMutex.Lock()
	defer c.plaMutex.Unlock()
	if c.player == nil {
		return
	}
	c.player.AddScore(score, correct, c.manager)
}

func (c *Connection) NextQ() {
	if c == nil {
		return
	}
	c.plaMutex.Lock()
	defer c.plaMutex.Unlock()
	if c.player == nil {
		return
	}
	c.player.ResetAnswered()
}

func (c *Connection) KickPlayer(requireDelete bool) bool {
	if c == nil {
		return false
	}
	c.plaMutex.Lock()
	defer c.plaMutex.Unlock()
	if c.player == nil {
		return false
	}
	defer func() { c.player = nil }()
	if requireDelete {
		return c.player.DeleteGuest(c.manager)
	}
	return true
}

func (c *Connection) HasExpired() bool {
	if c == nil {
		return false
	}
	return c.expires.Before(time.Now())
}

func (c *Connection) TimeTillExpiry() time.Duration {
	if c == nil {
		return 0
	}
	return time.Until(c.expires)
}

func (c *Connection) IsActive() bool {
	if c == nil {
		return false
	}
	return c.transport.IsActive()
}

func (c *Connection) Close() error {
	if c == nil {
		return nil
	}
	c.closeMutex.Lock()
	defer c.closeMutex.Unlock()
	if c.transport.IsActive() {
		//c.KickPlayer(true)
		close(c.intake)
		close(c.outtakeServer)
		close(c.outtakeGame)
		close(c.termChan)
	}
	return c.transport.Close()
}
