package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/sirupsen/logrus"

	"liga-discord-gc/internal/app"
	"liga-discord-gc/internal/dota"
)


// DotaClientFunc returns the current Dota client (may be nil).
type DotaClientFunc func() *dota.Client

type Server struct {
	app        *app.App
	getDota    DotaClientFunc
	logger     *logrus.Logger
	httpServer *http.Server
}

func New(port string, a *app.App, getDota DotaClientFunc, logger *logrus.Logger) *Server {
	s := &Server{
		app:     a,
		getDota: getDota,
		logger:  logger,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/lobby", s.handleLobby)
	mux.HandleFunc("/lobby/leave", s.handleLeaveLobby)
	mux.HandleFunc("/match/{id}", s.handleMatchDetails)

	s.httpServer = &http.Server{
		Addr:    ":" + port,
		Handler: mux,
	}

	return s
}

func (s *Server) Start() {
	s.logger.WithField("addr", s.httpServer.Addr).Info("[API] Servidor HTTP iniciado")
	if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		s.logger.WithError(err).Fatal("[API] Falha no servidor HTTP")
	}
}

func (s *Server) Shutdown(ctx context.Context) {
	s.logger.Info("[API] Encerrando servidor HTTP...")
	if err := s.httpServer.Shutdown(ctx); err != nil {
		s.logger.WithError(err).Warn("[API] Erro ao encerrar servidor HTTP")
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	s.logger.Debug("[API] GET /status")

	gcReady := s.app.IsGCReady()
	lobby := s.app.GetLobby()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"gc_ready": gcReady,
		"lobby":    lobby,
	})
}

func (s *Server) handleLobby(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.handleCreateLobby(w, r)
	case http.MethodDelete:
		s.handleDestroyLobby(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleCreateLobby(w http.ResponseWriter, r *http.Request) {
	s.logger.Info("[API] POST /lobby — solicitação de criação de lobby recebida")

	if !s.app.IsGCReady() {
		s.logger.Warn("[API] POST /lobby rejeitado — GC não está pronto")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "GC não está pronto"})
		return
	}

	d := s.getDota()
	if d == nil {
		s.logger.Warn("[API] POST /lobby rejeitado — cliente Dota não disponível")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "cliente Dota não disponível"})
		return
	}

	var req struct {
		Preset   string `json:"preset"`   // "inhouse" ou "1v1"
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	req.Preset = string(dota.PresetInhouse) // padrão

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
		s.logger.WithError(err).Warn("[API] POST /lobby — body inválido")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "body JSON inválido"})
		return
	}

	s.logger.WithFields(logrus.Fields{
		"preset":   req.Preset,
		"name":     req.Name,
		"password": req.Password,
	}).Info("[API] Encaminhando criação de lobby ao cliente Dota...")

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	lobbyReq := dota.LobbyRequest{
		Preset:   dota.Preset(req.Preset),
		Name:     req.Name,
		Password: req.Password,
	}

	if err := d.CreateLobby(ctx, lobbyReq); err != nil {
		s.logger.WithError(err).Error("[API] POST /lobby falhou")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	s.app.SetLobby(&app.LobbyInfo{Name: req.Name, Password: req.Password, Preset: req.Preset})
	s.logger.WithField("name", req.Name).Info("[API] Lobby criado — bot será movido para player pool via evento do GC")

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"ok":       true,
		"name":     req.Name,
		"password": req.Password,
	})
}

func (s *Server) handleLeaveLobby(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.logger.Info("[API] POST /lobby/leave — solicitação de saída do lobby recebida")

	if !s.app.IsGCReady() {
		s.logger.Warn("[API] POST /lobby/leave rejeitado — GC não está pronto")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "GC não está pronto"})
		return
	}

	d := s.getDota()
	if d == nil {
		s.logger.Warn("[API] POST /lobby/leave rejeitado — cliente Dota não disponível")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "cliente Dota não disponível"})
		return
	}

	d.LeaveLobby()
	s.app.SetLobby(nil)
	s.logger.Info("[API] Bot saiu do lobby")

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

type matchPlayerDTO struct {
	AccountID  uint32 `json:"account_id"`
	PlayerSlot uint32 `json:"player_slot"`
	HeroID     int32  `json:"hero_id"`
	Kills      uint32 `json:"kills"`
	Deaths     uint32 `json:"deaths"`
	Assists    uint32 `json:"assists"`
	NetWorth   uint32 `json:"net_worth"`
	Level      uint32 `json:"level"`
	PlayerName string `json:"player_name"`
}

type matchResultDTO struct {
	MatchID    uint64           `json:"match_id"`
	Duration   uint32           `json:"duration"`
	RadiantWin bool             `json:"radiant_win"`
	GameMode   int32            `json:"game_mode"`
	LobbyType  uint32           `json:"lobby_type"`
	Players    []matchPlayerDTO `json:"players"`
}

func (s *Server) handleMatchDetails(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	rawID := r.PathValue("id")
	matchID, err := strconv.ParseUint(rawID, 10, 64)
	if err != nil {
		http.Error(w, "match id inválido", http.StatusBadRequest)
		return
	}

	if !s.app.IsGCReady() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "GC não está pronto"})
		return
	}

	d := s.getDota()
	if d == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "cliente Dota não disponível"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	resp, err := d.GetMatchDetails(ctx, matchID)
	if err != nil {
		s.logger.WithError(err).Error("[API] GET /match falhou")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	match := resp.GetMatch()
	if match == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "partida não encontrada"})
		return
	}

	players := make([]matchPlayerDTO, 0, len(match.GetPlayers()))
	for _, p := range match.GetPlayers() {
		players = append(players, matchPlayerDTO{
			AccountID:  p.GetAccountId(),
			PlayerSlot: p.GetPlayerSlot(),
			HeroID:     p.GetHeroId(),
			Kills:      p.GetKills(),
			Deaths:     p.GetDeaths(),
			Assists:    p.GetAssists(),
			NetWorth:   p.GetNetWorth(),
			Level:      p.GetLevel(),
			PlayerName: p.GetPlayerName(),
		})
	}

	result := matchResultDTO{
		MatchID:    match.GetMatchId(),
		Duration:   match.GetDuration(),
		RadiantWin: match.GetMatchOutcome() == 2, // k_EMatchOutcome_RadiantVictory
		GameMode:   int32(match.GetGameMode()),
		LobbyType:  match.GetLobbyType(),
		Players:    players,
	}

	s.logger.WithFields(logrus.Fields{
		"match_id":   result.MatchID,
		"duration":   result.Duration,
		"radiant_win": result.RadiantWin,
		"players":    len(players),
	}).Info("[API] Detalhes da partida via GC")

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"result": result})
}

func (s *Server) handleDestroyLobby(w http.ResponseWriter, r *http.Request) {
	s.logger.Info("[API] DELETE /lobby — solicitação de encerramento de lobby recebida")

	if !s.app.IsGCReady() {
		s.logger.Warn("[API] DELETE /lobby rejeitado — GC não está pronto")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "GC não está pronto"})
		return
	}

	d := s.getDota()
	if d == nil {
		s.logger.Warn("[API] DELETE /lobby rejeitado — cliente Dota não disponível")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "cliente Dota não disponível"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	if err := d.DestroyLobby(ctx); err != nil {
		s.logger.WithError(err).Error("[API] DELETE /lobby falhou")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	s.app.SetLobby(nil)
	s.logger.Info("[API] Lobby destruído e removido do estado da app")

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}
