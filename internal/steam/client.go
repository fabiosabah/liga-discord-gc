package steam

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"time"

	dota2 "github.com/paralin/go-dota2"
	devents "github.com/paralin/go-dota2/events"
	"github.com/paralin/go-dota2/protocol"
	"github.com/paralin/go-dota2/socache"
	gosteam "github.com/paralin/go-steam"
	"github.com/paralin/go-steam/protocol/steamlang"
	"github.com/sirupsen/logrus"

	"liga-discord-gc/internal/app"
	"liga-discord-gc/internal/config"
	"liga-discord-gc/internal/dota"
)

// OnDotaReadyFunc is called when the Dota GC session is established.
type OnDotaReadyFunc func(client *dota.Client)

// retryState holds exponential backoff state for reconnections.
type retryState struct {
	current time.Duration
	max     time.Duration
}

func newRetryState() *retryState {
	return &retryState{current: 15 * time.Second, max: 5 * time.Minute}
}

func (r *retryState) Next() time.Duration {
	d := r.current
	r.current *= 2
	if r.current > r.max {
		r.current = r.max
	}
	return d
}

func (r *retryState) Reset() {
	r.current = 15 * time.Second
}

type Client struct {
	cfg         *config.Config
	app         *app.App
	logger      *logrus.Logger
	raw         *gosteam.Client
	dotaMu      sync.Mutex
	dotaClient  *dota.Client
	onReady     OnDotaReadyFunc
	retry       *retryState
	stopping    atomic.Bool
	shutdownCh  chan struct{}
	watchMu     sync.Mutex
	watchCancel context.CancelFunc
}

func New(cfg *config.Config, a *app.App, logger *logrus.Logger, onReady OnDotaReadyFunc) *Client {
	return &Client{
		cfg:        cfg,
		app:        a,
		logger:     logger,
		raw:        gosteam.NewClient(),
		onReady:    onReady,
		retry:      newRetryState(),
		shutdownCh: make(chan struct{}),
	}
}

// Connect initiates the TCP connection to Steam.
func (c *Client) Connect() {
	c.logger.Info("[Steam] Iniciando conexão com os servidores Steam...")
	c.raw.Connect()
}

// Disconnect closes the Steam connection gracefully and stops reconnect attempts.
func (c *Client) Disconnect() {
	c.logger.Info("[Steam] Desconectando e parando reconexões...")
	c.stopping.Store(true)
	close(c.shutdownCh)

	c.cancelLobbyWatcher()

	c.dotaMu.Lock()
	d := c.dotaClient
	c.dotaMu.Unlock()

	if d != nil {
		c.logger.Info("[Steam] Notificando GC que paramos de jogar Dota 2...")
		d.SetNotPlaying()
	}

	c.raw.Disconnect()
}

// RunEventLoop processes Steam + Dota GC events. Blocks until the process shuts down.
func (c *Client) RunEventLoop() {
	c.logger.Info("[Steam] Loop de eventos iniciado — aguardando eventos do Steam...")

	for event := range c.raw.Events() {
		c.handleEvent(event)
	}

	c.logger.Info("[Steam] Loop de eventos encerrado")
}

func (c *Client) handleEvent(event any) {
	switch e := event.(type) {

	case *gosteam.ConnectedEvent:
		c.onConnected()

	case *gosteam.LoggedOnEvent:
		c.onLoggedOn(e)

	case *gosteam.LogOnFailedEvent:
		c.logger.WithFields(logrus.Fields{
			"result": e.Result.String(),
			"code":   int(e.Result),
		}).Error("[Steam] Login falhou — verifique usuário, senha e 2FA")

	case *gosteam.MachineAuthUpdateEvent:
		c.onMachineAuth(e)

	case *gosteam.DisconnectedEvent:
		c.onDisconnected()

	case *devents.ClientWelcomed:
		c.onGCWelcomed(e)

	case *devents.GCConnectionStatusChanged:
		c.onGCStatusChanged(e)

	case error:
		c.logger.WithError(e).Error("[Steam] Erro recebido do cliente Steam")
	}
}

func (c *Client) onConnected() {
	c.logger.Info("[Steam] Conexão TCP estabelecida — enviando credenciais de login...")

	details := &gosteam.LogOnDetails{
		Username:      c.cfg.SteamUsername,
		Password:      c.cfg.SteamPassword,
		TwoFactorCode: c.cfg.Steam2FACode,
	}

	if sentry, err := os.ReadFile("sentry.bin"); err == nil {
		details.SentryFileHash = sentry
		c.logger.Info("[Steam] sentry.bin encontrado — Steam Guard por e-mail não será necessário")
	} else {
		c.logger.Info("[Steam] Nenhum sentry.bin — se Steam Guard estiver ativo, um e-mail será enviado")
	}

	c.raw.Auth.LogOn(details)
	c.logger.WithField("username", c.cfg.SteamUsername).Info("[Steam] Aguardando resposta de login do Steam...")
}

func (c *Client) onLoggedOn(e *gosteam.LoggedOnEvent) {
	c.logger.WithFields(logrus.Fields{
		"steamid":   c.raw.SteamId().ToString(),
		"public_ip": e.Body.GetPublicIp(),
	}).Info("[Steam] Login realizado com sucesso")

	c.retry.Reset()

	c.raw.Social.SetPersonaState(steamlang.EPersonaState_Online)

	c.logger.Info("[GC] Inicializando cliente Dota 2 GC...")
	rawDota := dota2.New(c.raw, c.logger)

	c.logger.Info("[GC] Notificando Steam que estamos jogando Dota 2 (AppID 570)...")
	rawDota.SetPlaying(true)

	dotaClient := dota.New(rawDota, c.logger)
	c.dotaMu.Lock()
	c.dotaClient = dotaClient
	c.dotaMu.Unlock()

	// SayHello em goroutine para não bloquear o event loop durante o sleep
	go func() {
		c.logger.Info("[GC] Aguardando 3s antes de enviar SayHello...")
		time.Sleep(3 * time.Second)

		c.dotaMu.Lock()
		still := c.dotaClient == dotaClient
		c.dotaMu.Unlock()
		if !still {
			c.logger.Info("[GC] Conexão encerrada durante espera — SayHello cancelado")
			return
		}

		c.logger.Info("[GC] Enviando SayHello ao Game Coordinator — aguardando sessão GC...")
		rawDota.SayHello()
	}()
}

func (c *Client) onMachineAuth(e *gosteam.MachineAuthUpdateEvent) {
	c.logger.Info("[Steam] Steam Guard: salvando sentry.bin para próximos logins...")
	if err := os.WriteFile("sentry.bin", e.Hash, 0600); err != nil {
		c.logger.WithError(err).Warn("[Steam] Falha ao salvar sentry.bin")
	} else {
		c.logger.Info("[Steam] sentry.bin salvo — próximos logins serão automáticos")
	}
}

func (c *Client) onDisconnected() {
	c.logger.Warn("[Steam] Desconectado dos servidores Steam")

	c.cancelLobbyWatcher()

	c.dotaMu.Lock()
	c.dotaClient = nil
	c.dotaMu.Unlock()

	c.app.SetGCReady(false)
	c.app.SetLobby(nil)

	c.logger.Info("[Steam] Estado GC resetado — lobby e sessão limpos")

	if !c.stopping.Load() {
		go c.reconnectLoop()
	}
}

func (c *Client) reconnectLoop() {
	if c.stopping.Load() {
		c.logger.Info("[Reconnect] Shutdown em andamento — cancelando reconexão")
		return
	}

	delay := c.retry.Next()
	c.logger.WithFields(logrus.Fields{
		"delay":    delay.String(),
		"next_max": c.retry.max.String(),
	}).Info("[Reconnect] Aguardando antes de reconectar ao Steam...")

	select {
	case <-c.shutdownCh:
		c.logger.Info("[Reconnect] Shutdown recebido durante espera — cancelando")
		return
	case <-time.After(delay):
	}

	c.logger.Info("[Reconnect] Tentando reconectar ao Steam...")
	c.raw.Connect()
}

func (c *Client) onGCWelcomed(e *devents.ClientWelcomed) {
	c.logger.WithField("version", e.Welcome.GetVersion()).
		Info("[GC] Bem-vindo recebido do Game Coordinator — sessão GC estabelecida!")

	c.app.SetGCReady(true)

	c.dotaMu.Lock()
	d := c.dotaClient
	c.dotaMu.Unlock()

	if d != nil {
		c.startLobbyWatcher(d)
	}

	if d != nil && c.onReady != nil {
		c.logger.Info("[GC] Notificando listeners que o GC está pronto...")
		c.onReady(d)
	}
}

func (c *Client) onGCStatusChanged(e *devents.GCConnectionStatusChanged) {
	c.logger.WithFields(logrus.Fields{
		"old_state": e.OldState.String(),
		"new_state": e.NewState.String(),
	}).Info("[GC] Status da conexão com o Game Coordinator mudou")

	if e.NewState == protocol.GCConnectionStatus_GCConnectionStatus_HAVE_SESSION {
		c.logger.Info("[GC] Game Coordinator pronto (HAVE_SESSION) — lobbies podem ser criados")
		c.app.SetGCReady(true)
	} else {
		c.logger.WithField("state", e.NewState.String()).
			Warn("[GC] Game Coordinator não disponível — aguardando reconexão")
		c.app.SetGCReady(false)
	}
}

func (c *Client) cancelLobbyWatcher() {
	c.watchMu.Lock()
	defer c.watchMu.Unlock()
	if c.watchCancel != nil {
		c.watchCancel()
		c.watchCancel = nil
	}
}

func (c *Client) startLobbyWatcher(d *dota.Client) {
	c.watchMu.Lock()
	if c.watchCancel != nil {
		c.watchCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.watchCancel = cancel
	c.watchMu.Unlock()

	botID := c.raw.SteamId().ToUint64()
	var prevMembers map[uint64]*protocol.CSODOTALobbyMember
	var launched bool

	err := d.WatchLobby(ctx, func(evType socache.EventType, lobby *protocol.CSODOTALobby) {
		c.handleLobbyEvent(evType, lobby, &prevMembers, &launched, botID, d)
	})
	if err != nil {
		c.logger.WithError(err).Error("[Lobby] Falha ao iniciar watcher de lobby")
		return
	}
	c.logger.Info("[Lobby] Watcher de lobby iniciado via SOCache")
}

func (c *Client) handleLobbyEvent(
	evType socache.EventType,
	lobby *protocol.CSODOTALobby,
	prevMembers *map[uint64]*protocol.CSODOTALobbyMember,
	launched *bool,
	botID uint64,
	d *dota.Client,
) {
	switch evType {
	case socache.EventTypeCreate:
		c.logger.WithField("name", lobby.GetGameName()).Info("[Lobby] Lobby criado")
		*launched = false
	case socache.EventTypeDestroy:
		c.logger.Info("[Lobby] Lobby destruído")
		*prevMembers = nil
		*launched = false
		return
	}

	curr := make(map[uint64]*protocol.CSODOTALobbyMember, len(lobby.GetAllMembers()))
	for _, m := range lobby.GetAllMembers() {
		curr[m.GetId()] = m
	}

	if *prevMembers != nil {
		for id, m := range curr {
			if _, existed := (*prevMembers)[id]; !existed {
				c.logger.WithFields(logrus.Fields{
					"steamid": id,
					"team":    m.GetTeam().String(),
					"slot":    m.GetSlot(),
				}).Info("[Lobby] Jogador entrou")
			}
		}
		for id, m := range *prevMembers {
			if _, exists := curr[id]; !exists {
				c.logger.WithFields(logrus.Fields{
					"steamid": id,
					"team":    m.GetTeam().String(),
					"slot":    m.GetSlot(),
				}).Info("[Lobby] Jogador saiu")
			}
		}
		for id, m := range curr {
			if old, existed := (*prevMembers)[id]; existed {
				if old.GetTeam() != m.GetTeam() || old.GetSlot() != m.GetSlot() {
					c.logger.WithFields(logrus.Fields{
						"steamid":  id,
						"old_team": old.GetTeam().String(),
						"old_slot": old.GetSlot(),
						"new_team": m.GetTeam().String(),
						"new_slot": m.GetSlot(),
					}).Info("[Lobby] Jogador mudou de time/slot")
				}
			}
		}
	}

	*prevMembers = curr

	if botMember, ok := curr[botID]; ok {
		team := botMember.GetTeam()
		if team == protocol.DOTA_GC_TEAM_DOTA_GC_TEAM_GOOD_GUYS ||
			team == protocol.DOTA_GC_TEAM_DOTA_GC_TEAM_BAD_GUYS {
			info := c.app.GetLobby()
			is1v1 := info != nil && info.Preset == "1v1"
			if is1v1 {
				c.logger.WithField("team", team.String()).
					Info("[Lobby] Bot detectado em slot de jogador (1v1) — movendo para player pool em 1s...")
				go func() {
					time.Sleep(time.Second)
					d.JoinPlayerPool()
				}()
			} else {
				c.logger.WithField("team", team.String()).
					Info("[Lobby] Bot detectado em slot de jogador — movendo para broadcast channel em 1s...")
				go func() {
					time.Sleep(time.Second)
					d.JoinBroadcastChannel()
				}()
			}
		}
	}

	// Auto-start para lobby 1v1: inicia quando Radiant e Dire têm 1 jogador cada
	if !*launched {
		if info := c.app.GetLobby(); info != nil && info.Preset == "1v1" {
			var goodGuys, badGuys int
			for id, m := range curr {
				if id == botID {
					continue
				}
				switch m.GetTeam() {
				case protocol.DOTA_GC_TEAM_DOTA_GC_TEAM_GOOD_GUYS:
					goodGuys++
				case protocol.DOTA_GC_TEAM_DOTA_GC_TEAM_BAD_GUYS:
					badGuys++
				}
			}
			if goodGuys >= 1 && badGuys >= 1 {
				*launched = true
				c.logger.WithFields(logrus.Fields{
					"good_guys": goodGuys,
					"bad_guys":  badGuys,
				}).Info("[Lobby] 1v1 pronto — iniciando partida em 2s...")
				go func() {
					time.Sleep(2 * time.Second)
					d.LaunchLobby()
					time.Sleep(time.Second)
					d.LeaveLobby()
					c.app.SetLobby(nil)
					c.logger.Info("[Lobby] Partida 1v1 iniciada — bot saiu do lobby")
				}()
			}
		}
	}
}

// GetDotaClient returns the current Dota GC client (nil if not connected).
func (c *Client) GetDotaClient() *dota.Client {
	c.dotaMu.Lock()
	defer c.dotaMu.Unlock()
	return c.dotaClient
}
