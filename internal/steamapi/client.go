package steamapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/sirupsen/logrus"
)

const matchDetailsURL = "https://api.steampowered.com/IDOTA2Match_570/GetMatchDetails/V1/"

type Client struct {
	apiKey     string
	httpClient *http.Client
	logger     *logrus.Logger
}

func New(apiKey string, logger *logrus.Logger) *Client {
	return &Client{
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 15 * time.Second},
		logger:     logger,
	}
}

// GetMatchDetails fetches raw match data from the Steam Web API and logs key fields.
// Returns the raw JSON body to be forwarded as-is to the caller.
func (c *Client) GetMatchDetails(ctx context.Context, matchID uint64) (json.RawMessage, error) {
	url := fmt.Sprintf("%s?match_id=%d&key=%s", matchDetailsURL, matchID, c.apiKey)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("steam api retornou %d: %s", resp.StatusCode, string(body))
	}

	// Loga campos-chave sem depender do caller
	c.logSummary(matchID, body)

	return json.RawMessage(body), nil
}

func (c *Client) logSummary(matchID uint64, body []byte) {
	var envelope struct {
		Result struct {
			Duration  int  `json:"duration"`
			RadiantWin bool `json:"radiant_win"`
			GameMode  int  `json:"game_mode"`
			LobbyType int  `json:"lobby_type"`
			Players   []struct {
				PlayerSlot int    `json:"player_slot"`
				HeroID     int    `json:"hero_id"`
				Kills      int    `json:"kills"`
				Deaths     int    `json:"deaths"`
				Assists    int    `json:"assists"`
				NetWorth   int    `json:"net_worth"`
				AccountID  uint64 `json:"account_id"`
			} `json:"players"`
		} `json:"result"`
	}

	if err := json.Unmarshal(body, &envelope); err != nil {
		c.logger.WithField("match_id", matchID).Warn("[SteamAPI] Não foi possível decodificar resposta para log")
		return
	}

	r := envelope.Result
	winner := "Dire"
	if r.RadiantWin {
		winner = "Radiant"
	}

	c.logger.WithFields(logrus.Fields{
		"match_id":   matchID,
		"duration":   r.Duration,
		"winner":     winner,
		"game_mode":  r.GameMode,
		"lobby_type": r.LobbyType,
		"players":    len(r.Players),
	}).Info("[SteamAPI] Detalhes da partida")

	for _, p := range r.Players {
		c.logger.WithFields(logrus.Fields{
			"match_id":   matchID,
			"slot":       p.PlayerSlot,
			"hero_id":    p.HeroID,
			"kills":      p.Kills,
			"deaths":     p.Deaths,
			"assists":    p.Assists,
			"net_worth":  p.NetWorth,
			"account_id": p.AccountID,
		}).Info("[SteamAPI] Jogador")
	}
}
