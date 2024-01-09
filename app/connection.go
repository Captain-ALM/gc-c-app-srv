package app

import (
	"crypto/sha256"
	"golang.local/gc-c-com/packet"
	"golang.local/gc-c-com/transport"
	"golang.local/gc-c-db/db"
	"sync"
	"time"
)

func NewConnection(manager *db.Manager, transport transport.Transport, expiry time.Time, sBuffAmnt int) *Connection {
	nConn := &Connection{
		manager:           manager,
		transport:         transport,
		intake:            make(chan *packet.Packet, sBuffAmnt),
		outtakeServer:     make(chan *packet.Packet),
		outtakeGame:       make(chan *packet.Packet),
		outtakeGameUNotif: make(chan bool),
		Session:           nil,
		plaMutex:          &sync.Mutex{},
		player:            nil,
		expires:           expiry,
		closeMutex:        &sync.Mutex{},
		termChan:          make(chan bool),
	}
	go nConn.intakePump()
	go nConn.outtakePump()
	go nConn.outtakeNoGameDropPump()
	return nConn
}

type Connection struct {
	manager           *db.Manager
	transport         transport.Transport
	intake            chan *packet.Packet
	outtakeServer     chan *packet.Packet
	outtakeGame       chan *packet.Packet
	outtakeGameUNotif chan bool
	gameActive        bool
	Session           *Session
	plaMutex          *sync.Mutex
	player            *Player
	expires           time.Time
	closeMutex        *sync.Mutex
	termChan          chan bool
}

func (c *Connection) GetID() transport.Transport {
	if c == nil {
		return nil
	}
	return c.transport
}

func (c *Connection) GetPlayerID() uint32 {
	if c == nil || c.player == nil {
		return 0
	}
	return c.player.GetID()
}

func (c *Connection) GetGameID() uint32 {
	if c == nil || c.player == nil {
		return 0
	}
	return c.player.GetGameID()
}

func (c *Connection) GetNickname() string {
	if c == nil || c.player == nil {
		return ""
	}
	return c.player.GetNickname()
}

func (c *Connection) GetIntake() chan<- *packet.Packet {
	if c == nil {
		return nil
	}
	return c.intake
}

func (c *Connection) intakePump() {
	for c.transport.IsActive() {
		select {
		case <-c.termChan:
			return
		case pk := <-c.intake:
			_ = c.transport.Send(pk)
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
			go func() {
				select {
				case <-c.termChan:
				case c.outtakeServer <- pk:
				}
				cWG.Done()
			}()
			go func() {
				select {
				case <-c.termChan:
				case c.outtakeGame <- pk:
				}
				cWG.Done()
			}()
			cWG.Wait()
		} else {
			DebugErrIsNil(err)
			return
		}
	}
}

func (c *Connection) outtakeNoGameDropPump() {
	for c.transport.IsActive() {
		if c.gameActive {
			select {
			case <-c.termChan:
				return
			case <-c.outtakeGameUNotif:
			}
		} else {
			select {
			case <-c.termChan:
				return
			case <-c.outtakeGameUNotif:
			case <-c.outtakeGame:
			}
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

func (c *Connection) RejoinGame(guestID uint32) (success bool, isTheHost bool) {
	if c == nil || guestID == 0 {
		return false, false
	}
	c.plaMutex.Lock()
	defer c.plaMutex.Unlock()
	if c.player != nil {
		return false, false
	}
	c.player = NewPlayer(guestID, "", 0, c.manager)
	return c.player != nil, c.player.IsHost()
}

func (c *Connection) AddScore(amount uint32, correct bool, streakEnabled bool) (score uint32, streak uint32) {
	if c == nil {
		return 0, 0
	}
	c.plaMutex.Lock()
	defer c.plaMutex.Unlock()
	if c.player == nil {
		return 0, 0
	}
	return c.player.AddScore(amount, correct, streakEnabled, c.manager)
}

func (c *Connection) GetScore() (score uint32, streak uint32) {
	if c == nil || c.player == nil {
		return 0, 0
	}
	return c.player.GetScore()
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
		close(c.termChan)
		//close(c.intake)
		//close(c.outtakeServer)
		//close(c.outtakeGame)
	}
	return c.transport.Close()
}

func (c *Connection) EnteredGame() {
	if c == nil {
		return
	}
	c.gameActive = true
	select {
	case <-c.termChan:
	case c.outtakeGameUNotif <- true:
	}
}

func (c *Connection) LeftGame() {
	if c == nil {
		return
	}
	c.gameActive = false
	select {
	case <-c.termChan:
	case c.outtakeGameUNotif <- false:
	}
}
