package app

import (
	"golang.local/gc-c-com/packet"
	"golang.local/gc-c-com/packets"
	"golang.local/gc-c-com/transport"
	"golang.local/gc-c-db/db"
	"golang.local/gc-c-db/tables"
	"sort"
	"sync"
	"time"
)

func NewGame(manager *db.Manager, onEnd func(game *Game), hostConn *Connection, quiz *packets.QuizDataPayload, serverID uint32, countdownMax uint32, streakEnabled bool, expiry time.Time) *Game {
	if manager == nil || hostConn == nil || quiz == nil {
		return nil
	}
	gamMeta := tables.Game{
		QuizID:        quiz.ID,
		ServerID:      serverID,
		CountdownMax:  countdownMax,
		StreakEnabled: streakEnabled,
		Expiry:        expiry,
		State:         byte(GameStateLobby),
	}
	err := manager.Save(&gamMeta)
	if err != nil {
		DebugPrintln(err.Error())
		return nil
	}
	ok := hostConn.HostGame(gamMeta.ID)
	if !ok {
		err := manager.Delete(gamMeta.GetIDObject())
		DebugErrIsNil(err)
		return nil
	}
	conns := make(map[transport.Transport]*Connection)
	conns[hostConn.GetID()] = hostConn
	nGam := &Game{
		manager:         manager,
		connections:     conns,
		hostConn:        hostConn,
		connChangeNotif: make(chan *packets.HostedGamePayload),
		hostConnNotif:   make(chan bool),
		proceedNotif:    make(chan bool),
		answerNotif:     make(chan uint32),
		answerCMutex:    &sync.Mutex{},
		conRWMutex:      &sync.RWMutex{},
		metadata:        gamMeta,
		quizQuestions:   quiz.Questions,
		quizAnswers:     quiz.Answers,
		endCallback:     onEnd,
		closeMutex:      &sync.Mutex{},
		termChan:        make(chan bool),
	}
	go nGam.gameSendLoop()
	go nGam.hostRecvLoop(hostConn)
	return nGam
}

type Game struct {
	manager         *db.Manager
	connections     map[transport.Transport]*Connection
	hostConn        *Connection
	connChangeNotif chan *packets.HostedGamePayload
	hostConnNotif   chan bool
	proceedNotif    chan bool
	answerNotif     chan uint32
	answerCount     uint32
	answerCMutex    *sync.Mutex
	conRWMutex      *sync.RWMutex
	metadata        tables.Game
	quizQuestions   packets.QuizQuestions
	quizAnswers     packets.QuizAnswers
	endCallback     func(game *Game)
	closeMutex      *sync.Mutex
	termChan        chan bool
	countdownValue  uint32
}

func (g *Game) GetID() uint32 {
	if g == nil {
		return 0
	}
	return g.metadata.ID
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
	if g.metadata.State == byte(GameStateLobby) {
		select {
		case <-g.termChan:
		case g.connChangeNotif <- g.getHostedGame():
		}
	}
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
	select {
	case <-g.termChan:
	case g.hostConnNotif <- true:
	}
	return true
}

func (g *Game) RemoveConnection(conn *Connection) bool {
	if g == nil || conn == nil {
		return false
	}
	g.conRWMutex.Lock()
	defer g.conRWMutex.Unlock()
	if conn == g.hostConn {
		select {
		case <-g.termChan:
		case g.hostConnNotif <- false:
		}
		g.hostConn = nil
	} else if g.metadata.State == byte(GameStateLobby) {
		select {
		case <-g.termChan:
		case g.connChangeNotif <- g.getHostedGame():
		}
	}
	delete(g.connections, conn.GetID())
	return true
}

func (g *Game) gameSendLoop() {
	defer func() { _ = g.Close() }()
	var cDownFinChan chan bool
	var cDownStopChan chan bool
	var cDownWG *sync.WaitGroup
	for g.IsActive() {
		switch GameState(g.metadata.State) {
		case GameStateLobby:
			select {
			case <-g.termChan:
				return
			case st := <-g.hostConnNotif:
				if g.waitForHostReconnect(st) {
					return
				}
			case hgPyl := <-g.connChangeNotif:
				g.sendHGPToAll(hgPyl)
			case <-g.answerNotif:
			case <-g.proceedNotif:
				if g.metadata.QuestionNo+1 > uint32(len(g.quizQuestions.Questions)) {
					return
				}
				g.metadata.QuestionNo = 1
				g.zeroQuestionsAnswered()
				g.metadata.State = byte(GameStateQuestion)
				err := g.manager.Save(&g.metadata)
				if err != nil {
					DebugPrintln(err.Error())
					return
				}
			}
		case GameStateQuestion:
			g.sendToAll(packet.FromNew(packets.NewGameQuestion(g.quizQuestions.Questions[g.metadata.QuestionNo], g.quizAnswers.Answers[g.metadata.QuestionNo], nil)))
			cDownFinChan = make(chan bool)
			cDownStopChan = make(chan bool)
			cDownWG = &sync.WaitGroup{}
			cDownWG.Add(1)
			g.metadata.State = byte(GameStateQuestionWait)
			err := g.manager.Save(&g.metadata)
			if err != nil {
				DebugPrintln(err.Error())
				return
			}
			go g.countdownProcessor(cDownFinChan, cDownStopChan, cDownWG)
		case GameStateQuestionWait:
			select {
			case <-g.termChan:
				cDownWG.Wait()
				return
			case st := <-g.hostConnNotif:
				if g.waitForHostReconnect(st) {
					return
				}
			case <-g.answerNotif:
				if g.answerCount >= g.getConnectionCount()-1 {
					close(cDownStopChan)
					cDownWG.Wait()
					g.metadata.State = byte(GameStateAnswerShow)
					err := g.manager.Save(&g.metadata)
					if err != nil {
						DebugPrintln(err.Error())
						return
					}
				}
			case <-g.connChangeNotif:
			case <-g.proceedNotif:
				close(cDownStopChan)
				cDownWG.Wait()
				g.metadata.State = byte(GameStateAnswerShow)
				err := g.manager.Save(&g.metadata)
				if err != nil {
					DebugPrintln(err.Error())
					return
				}
			case <-cDownFinChan:
				cDownWG.Wait()
				g.metadata.State = byte(GameStateAnswerShow)
				err := g.manager.Save(&g.metadata)
				if err != nil {
					DebugPrintln(err.Error())
					return
				}
			}
		case GameStateAnswerShow:
			g.sendToAll(packet.FromNew(packets.NewGameAnswer(g.quizAnswers.Answers[g.metadata.QuestionNo].CorrectAnswer, g.metadata.QuestionNo, nil)))
			g.metadata.State = byte(GameStateAnswerShowWait)
			err := g.manager.Save(&g.metadata)
			if err != nil {
				DebugPrintln(err.Error())
				return
			}
		case GameStateAnswerShowWait:
			select {
			case <-g.termChan:
				return
			case st := <-g.hostConnNotif:
				if g.waitForHostReconnect(st) {
					return
				}
			case <-g.answerNotif:
			case <-g.connChangeNotif:
			case <-g.proceedNotif:
				g.metadata.State = byte(GameStateLeaderboard)
				err := g.manager.Save(&g.metadata)
				if err != nil {
					DebugPrintln(err.Error())
					return
				}
			}
		case GameStateLeaderboard:
			g.sendToAll(packet.FromNew(packets.NewGameLeaderboard(g.getLeaderboard(), nil)))
			g.metadata.State = byte(GameStateLeaderboardWait)
			err := g.manager.Save(&g.metadata)
			if err != nil {
				DebugPrintln(err.Error())
				return
			}
		case GameStateLeaderboardWait:
			select {
			case <-g.termChan:
				return
			case st := <-g.hostConnNotif:
				if g.waitForHostReconnect(st) {
					return
				}
			case <-g.answerNotif:
			case <-g.connChangeNotif:
			case <-g.proceedNotif:
				if g.metadata.QuestionNo+1 > uint32(len(g.quizQuestions.Questions)) {
					return
				}
				g.metadata.QuestionNo += 1
				g.signalNextQ()
				g.zeroQuestionsAnswered()
				g.metadata.State = byte(GameStateQuestion)
				err := g.manager.Save(&g.metadata)
				if err != nil {
					DebugPrintln(err.Error())
					return
				}
			}
		}
	}
}

func (g *Game) countdownProcessor(finChan chan bool, stopChan chan bool, wg *sync.WaitGroup) {
	defer close(finChan)
	cDownT := time.NewTicker(time.Second)
	defer cDownT.Stop()
	g.countdownValue = g.metadata.CountdownMax
	g.sendToAll(packet.FromNew(packets.NewGameCountdown(g.countdownValue, nil)))
	for g.countdownValue > 0 {
		select {
		case <-g.termChan:
			g.countdownValue = 0
			wg.Done()
			return
		case <-stopChan:
			g.countdownValue = 0
			wg.Done()
			return
		case <-cDownT.C:
			g.countdownValue -= 1
			g.sendToAll(packet.FromNew(packets.NewGameCountdown(g.countdownValue, nil)))
		}
	}
	wg.Done()
	select {
	case <-g.termChan:
	case <-stopChan:
	case finChan <- true:
	}
}

func (g *Game) waitForHostReconnect(st bool) (terminate bool) {
	for !st {
		select {
		case <-g.termChan:
			return true
		case st = <-g.hostConnNotif:
		case <-g.connChangeNotif:
		case <-g.proceedNotif:
		case <-g.answerNotif:
		}
	}
	return false
}

func (g *Game) hostRecvLoop(conn *Connection) {
	defer g.RemoveConnection(conn)
	defer conn.LeftGame()
	conn.EnteredGame()
	for g.IsActive() && conn.IsActive() {
		select {
		case <-g.termChan:
			return
		case <-conn.GetTerminationChannel():
			return
		case pk := <-conn.GetOuttakeGame():
			switch pk.GetCommand() {
			case packets.GameLeave:
				fallthrough
			case packets.GameEnd:
				return
			case packets.GameProceed:
				select {
				case <-g.termChan:
					return
				case <-conn.GetTerminationChannel():
					return
				case g.proceedNotif <- true:
				}
			case packets.KickGuest:
				var pyl packets.IDPayload
				err := pk.GetPayload(&pyl)
				if err == nil && pyl.ID > 0 {
					kConn := g.getConnectionFromID(pyl.ID)
					ForkedSend(kConn, packet.FromNew(packets.NewGameError("Kicked", nil)))
					ForkedSend(kConn, packet.FromNew(packets.NewIDGuest(0, nil)))
					g.RemoveConnection(kConn)
					kConn.KickPlayer(true)
				}
			}
		}
	}
}

func (g *Game) guestRecvLoop(conn *Connection) {
	defer g.RemoveConnection(conn)
	defer conn.LeftGame()
	conn.EnteredGame()
	for g.IsActive() && conn.IsActive() {
		select {
		case <-g.termChan:
			return
		case <-conn.GetTerminationChannel():
			return
		case pk := <-conn.GetOuttakeGame():
			switch pk.GetCommand() {
			case packets.GameLeave:
				fallthrough
			case packets.GameEnd:
				return
			case packets.GameCommit:
				if g.metadata.State == byte(GameStateQuestion) || g.metadata.State == byte(GameStateQuestionWait) {
					var pyl packets.GameAnswerPayload
					err := pk.GetPayload(&pyl)
					if err == nil && pyl.QuestionNumber == g.metadata.QuestionNo {
						conn.AddScore(g.getConnectionCount()-g.answerCount-1, pyl.Index == g.quizAnswers.Answers[g.metadata.QuestionNo].CorrectAnswer, g.metadata.StreakEnabled)
						g.questionAnswered(pyl.QuestionNumber)
					}
				}
			}
		}
	}
}

func (g *Game) questionAnswered(qNum uint32) {
	if g == nil || qNum == 0 {
		return
	}
	g.answerCMutex.Lock()
	defer g.answerCMutex.Unlock()
	if qNum == g.metadata.QuestionNo {
		g.answerCount += 1
		select {
		case <-g.termChan:
		case g.answerNotif <- g.answerCount:
		}
	}
}

func (g *Game) zeroQuestionsAnswered() {
	if g == nil {
		return
	}
	g.answerCMutex.Lock()
	defer g.answerCMutex.Unlock()
	g.answerCount = 0
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
		_ = g.manager.Delete(g.metadata.GetIDObject())
		g.kickAll()
		close(g.termChan)
		//close(g.hostConnNotif)
		//close(g.proceedNotif)
		//close(g.answerNotif)
		//close(g.connChangeNotif)
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
		ForkedSend(cc, packet.FromNew(packets.NewIDGuest(0, nil)))
		cc.KickPlayer(false)
	}
}

func (g *Game) getConnectionFromID(tID uint32) *Connection {
	g.conRWMutex.RLock()
	defer g.conRWMutex.RUnlock()
	for _, cc := range g.connections {
		if cc.GetPlayerID() == tID && cc != g.hostConn {
			return cc
		}
	}
	return nil
}

func (g *Game) getConnectionCount() uint32 {
	g.conRWMutex.RLock()
	defer g.conRWMutex.RUnlock()
	return uint32(len(g.connections))
}

func (g *Game) getHostedGame() *packets.HostedGamePayload {
	toRet := &packets.HostedGamePayload{ID: g.metadata.ID}
	for _, cc := range g.connections {
		if cc != g.hostConn {
			toRet.Guests = append(toRet.Guests, packets.JoinGamePayload{ID: cc.GetPlayerID(), Nickname: cc.GetNickname()})
		}
	}
	return toRet
}

func (*Game) getHostedGameGuestCopy(guestID uint32, hgPyl *packets.HostedGamePayload) *packets.HostedGamePayload {
	if hgPyl == nil {
		return nil
	}
	return &packets.HostedGamePayload{ID: hgPyl.ID, GuestID: guestID, Guests: hgPyl.Guests}
}

func (g *Game) sendToAll(pk *packet.Packet) {
	g.conRWMutex.RLock()
	defer g.conRWMutex.RUnlock()
	for _, cc := range g.connections {
		ForkedSend(cc, pk)
	}
}

func (g *Game) sendHGPToAll(hgPyl *packets.HostedGamePayload) {
	g.conRWMutex.RLock()
	defer g.conRWMutex.RUnlock()
	for _, cc := range g.connections {
		ForkedSend(cc, packet.FromNew(packet.New(packets.HostedGame, g.getHostedGameGuestCopy(cc.GetPlayerID(), hgPyl), nil)))
	}
}

func (g *Game) getLeaderboard() []packets.GameLeaderboardEntry {
	g.conRWMutex.RLock()
	defer g.conRWMutex.RUnlock()
	var lBoard []packets.GameLeaderboardEntry
	for _, cc := range g.connections {
		if cc != g.hostConn {
			cScore, cStreak := cc.GetScore()
			lBoard = append(lBoard, packets.GameLeaderboardEntry{
				ID:       cc.GetPlayerID(),
				Nickname: cc.GetNickname(),
				Score:    cScore,
				Streak:   cStreak,
			})
		}
	}
	sort.Slice(lBoard, func(i, j int) bool {
		return lBoard[i].Score < lBoard[j].Score
	})
	return lBoard
}

func (g *Game) sendScoresToAllGuests() {
	g.conRWMutex.RLock()
	defer g.conRWMutex.RUnlock()
	for _, cc := range g.connections {
		if cc != g.hostConn {
			cScore, _ := cc.GetScore()
			ForkedSend(cc, packet.FromNew(packets.NewGameScore(cScore, nil)))
		}
	}
}

func (g *Game) signalNextQ() {
	g.conRWMutex.RLock()
	defer g.conRWMutex.RUnlock()
	for _, cc := range g.connections {
		if cc != g.hostConn {
			cc.NextQ()
		}
	}
}

type GameState byte

const (
	GameStateLobby           = GameState(1)
	GameStateQuestion        = GameState(2)
	GameStateQuestionWait    = GameState(3)
	GameStateAnswerShow      = GameState(4)
	GameStateAnswerShowWait  = GameState(5)
	GameStateLeaderboard     = GameState(6)
	GameStateLeaderboardWait = GameState(7)
	GameStateFinish          = GameState(8)
)
