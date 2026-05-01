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
	cfg        *config.Config
	app        *app.App
	logger     *logrus.Logger
	raw        *gosteam.Client
	dotaMu     sync.Mutex
	dotaClient *dota.Client
	onReady    OnDotaReadyFunc
	retry      *retryState
	stopping   atomic.Bool
	shutdownCh chan struct{}
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

func (c *Client) handleEvent(event interface{}) {
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
		// Não usa Fatal para não matar o processo — reconexão vai tentar novamente

	case *gosteam.MachineAuthUpdateEvent:
		c.onMachineAuth(e)

	case *gosteam.DisconnectedEvent:
		c.onDisconnected()

	case *devents.ClientWelcomed:
		c.onGCWelcomed(e)

	case *devents.GCConnectionStatusChanged:
		c.onGCStatusChanged(e)

	case devents.ClientStateChanged:
		c.onLobbyStateChanged(e)

	case error:
		// FatalErrorEvent é `type FatalErrorEvent error`; steam desconecta automaticamente
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
	c.logger.Info("[Steam] Contador de reconexão resetado após login bem-sucedido")

	c.logger.Info("[Steam] Definindo estado de persona para Online...")
	c.raw.Social.SetPersonaState(steamlang.EPersonaState_Online)

	c.logger.Info("[GC] Inicializando cliente Dota 2 GC...")
	rawDota := dota2.New(c.raw, c.logger)

	c.logger.Info("[GC] Notificando Steam que estamos jogando Dota 2 (AppID 570)...")
	rawDota.SetPlaying(true)

	c.logger.Info("[GC] Aguardando 3s antes de enviar SayHello...")
	time.Sleep(3 * time.Second)

	c.logger.Info("[GC] Enviando SayHello ao Game Coordinator — aguardando sessão GC...")
	rawDota.SayHello()

	dotaClient := dota.New(rawDota, c.logger)

	c.dotaMu.Lock()
	c.dotaClient = dotaClient
	c.dotaMu.Unlock()
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
	for {
		if c.stopping.Load() {
			c.logger.Info("[Reconnect] Shutdown em andamento — cancelando reconexão")
			return
		}

		delay := c.retry.Next()
		c.logger.WithFields(logrus.Fields{
			"delay":      delay.String(),
			"next_max":   c.retry.max.String(),
		}).Info("[Reconnect] Aguardando antes de reconectar ao Steam...")

		select {
		case <-c.shutdownCh:
			c.logger.Info("[Reconnect] Shutdown recebido durante espera — cancelando")
			return
		case <-time.After(delay):
		}

		c.logger.Info("[Reconnect] Tentando reconectar ao Steam...")
		c.raw.Connect()
		return // sai do loop — próxima desconexão vai chamar reconnectLoop novamente
	}
}

func (c *Client) onGCWelcomed(e *devents.ClientWelcomed) {
	c.logger.WithField("version", e.Welcome.GetVersion()).
		Info("[GC] Bem-vindo recebido do Game Coordinator — sessão GC estabelecida!")

	c.app.SetGCReady(true)

	c.dotaMu.Lock()
	d := c.dotaClient
	c.dotaMu.Unlock()

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

func (c *Client) onLobbyStateChanged(e devents.ClientStateChanged) {
	oldLobby := e.OldState.Lobby
	newLobby := e.NewState.Lobby

	if oldLobby == nil && newLobby != nil {
		c.logger.WithField("name", newLobby.GetGameName()).Info("[Lobby] Lobby criado")
	} else if oldLobby != nil && newLobby == nil {
		c.logger.Info("[Lobby] Lobby encerrado")
		return
	}

	if newLobby == nil {
		return
	}

	// Indexa membros antigos por ID para detectar mudanças
	oldMembers := make(map[uint64]*protocol.CSODOTALobbyMember, len(oldLobby.GetAllMembers()))
	for _, m := range oldLobby.GetAllMembers() {
		oldMembers[m.GetId()] = m
	}

	botID := c.raw.SteamId().ToUint64()

	for _, m := range newLobby.GetAllMembers() {
		id := m.GetId()
		team := m.GetTeam()

		if old, ok := oldMembers[id]; !ok {
			c.logger.WithFields(logrus.Fields{
				"steamid": id,
				"team":    team.String(),
				"slot":    m.GetSlot(),
			}).Info("[Lobby] Jogador entrou")
		} else if old.GetTeam() != team || old.GetSlot() != m.GetSlot() {
			c.logger.WithFields(logrus.Fields{
				"steamid":  id,
				"old_team": old.GetTeam().String(),
				"old_slot": old.GetSlot(),
				"new_team": team.String(),
				"new_slot": m.GetSlot(),
			}).Info("[Lobby] Jogador mudou de time/slot")
		}

		delete(oldMembers, id)

		// Se o bot foi parar num slot de jogador, move para player pool
		if id == botID {
			if team == protocol.DOTA_GC_TEAM_DOTA_GC_TEAM_GOOD_GUYS ||
				team == protocol.DOTA_GC_TEAM_DOTA_GC_TEAM_BAD_GUYS {
				c.logger.WithField("team", team.String()).
					Info("[Lobby] Bot detectado em slot de jogador — movendo para player pool...")
				c.dotaMu.Lock()
				d := c.dotaClient
				c.dotaMu.Unlock()
				if d != nil {
					d.JoinPlayerPool()
				}
			}
		}
	}

	for id, m := range oldMembers {
		c.logger.WithFields(logrus.Fields{
			"steamid": id,
			"team":    m.GetTeam().String(),
			"slot":    m.GetSlot(),
		}).Info("[Lobby] Jogador saiu")
	}
}

// GetDotaClient returns the current Dota GC client (nil if not connected).
func (c *Client) GetDotaClient() *dota.Client {
	c.dotaMu.Lock()
	defer c.dotaMu.Unlock()
	return c.dotaClient
}
