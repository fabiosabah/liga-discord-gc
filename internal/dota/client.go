package dota

import (
	"context"
	"fmt"

	dota2 "github.com/paralin/go-dota2"
	"github.com/paralin/go-dota2/cso"
	"github.com/paralin/go-dota2/protocol"
	"github.com/paralin/go-dota2/socache"
	"github.com/sirupsen/logrus"
)

// Preset define o tipo de lobby a ser criado.
type Preset string

const (
	// PresetInhouse: Captains Mode para 10 jogadores (partida de liga).
	PresetInhouse Preset = "inhouse"
	// Preset1v1: Solo Mid para testes rápidos.
	Preset1v1 Preset = "1v1"
)

// LobbyRequest contém os parâmetros variáveis de uma sala.
type LobbyRequest struct {
	Preset   Preset // "inhouse" ou "1v1"
	Name     string // nome da sala (padrão: "UEFA FUMOS LEAGUE")
	Password string // senha da sala (padrão: "1234")
}

// lobbySettings são os parâmetros fixos de cada preset.
type lobbySettings struct {
	gameMode    protocol.DOTA_GameMode
	serverRegion uint32
	name        string
}

var presets = map[Preset]lobbySettings{
	PresetInhouse: {
		gameMode:     protocol.DOTA_GameMode_DOTA_GAMEMODE_CM,
		serverRegion: 10,
		name:         "UEFA FUMOS LEAGUE",
	},
	Preset1v1: {
		gameMode:     protocol.DOTA_GameMode_DOTA_GAMEMODE_1V1MID,
		serverRegion: 10,
		name:         "UEFA FUMOS 1v1",
	},
}

type Client struct {
	d      *dota2.Dota2
	logger *logrus.Logger
}

func New(d *dota2.Dota2, logger *logrus.Logger) *Client {
	logger.Info("[Dota] Cliente Dota 2 GC inicializado")
	return &Client{d: d, logger: logger}
}

func (c *Client) CreateLobby(ctx context.Context, req LobbyRequest) error {
	settings, ok := presets[req.Preset]
	if !ok {
		return fmt.Errorf("preset desconhecido: %q (use 'inhouse' ou '1v1')", req.Preset)
	}

	name := req.Name
	if name == "" {
		name = settings.name
	}
	password := req.Password
	if password == "" {
		password = "1234"
	}

	// Destrói lobby existente antes de criar um novo
	c.logger.Info("[Dota] Destruindo lobby anterior (se existir)...")
	if _, err := c.d.DestroyLobby(ctx); err != nil {
		c.logger.WithError(err).Debug("[Dota] Nenhum lobby ativo para destruir (normal)")
	}

	c.logger.WithFields(logrus.Fields{
		"preset":   req.Preset,
		"name":     name,
		"mode":     settings.gameMode.String(),
		"region":   settings.serverRegion,
		"password": password,
	}).Info("[Dota] Criando lobby...")

	gameMode := uint32(settings.gameMode)
	region := settings.serverRegion
	allowCheats := false
	fillWithBots := false
	allowSpec := true
	allChat := true
	lan := false
	tvDelay := protocol.LobbyDotaTVDelay_LobbyDotaTV_10
	pausePolicy := protocol.LobbyDotaPauseSetting_LobbyDotaPauseSetting_Limited
	vis := protocol.DOTALobbyVisibility_DOTALobbyVisibility_Public

	details := &protocol.CMsgPracticeLobbySetDetails{
		GameName:        &name,
		PassKey:         &password,
		ServerRegion:    &region,
		GameMode:        &gameMode,
		AllowCheats:     &allowCheats,
		FillWithBots:    &fillWithBots,
		AllowSpectating: &allowSpec,
		Allchat:         &allChat,
		Lan:             &lan,
		DotaTvDelay:     &tvDelay,
		PauseSetting:    &pausePolicy,
		Visibility:      &vis,
	}

	if err := c.d.LeaveCreateLobby(ctx, details, true); err != nil {
		c.logger.WithError(err).Error("[Dota] Falha ao criar lobby")
		return err
	}

	c.logger.WithFields(logrus.Fields{
		"name":   name,
		"preset": req.Preset,
	}).Info("[Dota] Lobby criado com sucesso")
	return nil
}

func (c *Client) SetNotPlaying() {
	c.logger.Info("[Dota] Notificando Steam que paramos de jogar Dota 2...")
	c.d.SetPlaying(false)
}

func (c *Client) JoinBroadcastChannel() {
	c.logger.Info("[Dota] Movendo bot para canal de broadcast (caster)...")
	c.d.JoinLobbyBroadcastChannel(0, "", "", "")
}

func (c *Client) LeaveLobby() {
	c.logger.Info("[Dota] Saindo do lobby...")
	c.d.LeaveLobby()
}

func (c *Client) LaunchLobby() {
	c.logger.Info("[Dota] Iniciando partida...")
	c.d.LaunchLobby()
}

func (c *Client) DestroyLobby(ctx context.Context) error {
	c.logger.Info("[Dota] Destruindo lobby...")

	if _, err := c.d.DestroyLobby(ctx); err != nil {
		c.logger.WithError(err).Error("[Dota] Falha ao destruir lobby")
		return err
	}

	c.logger.Info("[Dota] Lobby destruído")
	return nil
}

// WatchLobby subscribes to lobby SOCache events and calls onChange for each one.
// Runs in a background goroutine; returns error only if subscription fails immediately.
func (c *Client) WatchLobby(ctx context.Context, onChange func(socache.EventType, *protocol.CSODOTALobby)) error {
	ch, unsub, err := c.d.GetCache().SubscribeType(cso.Lobby)
	if err != nil {
		return err
	}

	go func() {
		defer unsub()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-ch:
				if !ok {
					return
				}
				if lobby, ok := ev.Object.(*protocol.CSODOTALobby); ok {
					onChange(ev.EventType, lobby)
				}
			}
		}
	}()

	return nil
}
