package app

import "sync"

// LobbyPlayer represents an invited player with their Discord identity.
type LobbyPlayer struct {
	SteamFriendID uint64 `json:"steam_friend_id"`
	DiscordName   string `json:"discord_name"`
}

type LobbyInfo struct {
	Name     string        `json:"name"`
	Password string        `json:"password"`
	Preset   string        `json:"preset"`
	LobbyID  uint64        `json:"lobby_id,omitempty"`
	Players  []LobbyPlayer `json:"players,omitempty"`
}

// LobbyStatus is the live state of the lobby, updated by the SOCache watcher.
type LobbyStatus struct {
	RadiantCaptain *LobbyPlayer   `json:"radiant_captain"`
	DireCaptain    *LobbyPlayer   `json:"dire_captain"`
	InPool         []LobbyPlayer  `json:"in_pool"`
	Missing        []string       `json:"missing"` // discord names not yet in lobby
}

// App holds shared runtime state accessible by all layers.
type App struct {
	mu          sync.RWMutex
	gcReady     bool
	lobby       *LobbyInfo
	lobbyStatus *LobbyStatus
}

func New() *App {
	return &App{}
}

func (a *App) SetGCReady(ready bool) {
	a.mu.Lock()
	a.gcReady = ready
	a.mu.Unlock()
}

func (a *App) IsGCReady() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.gcReady
}

func (a *App) SetLobby(l *LobbyInfo) {
	a.mu.Lock()
	a.lobby = l
	if l == nil {
		a.lobbyStatus = nil
	}
	a.mu.Unlock()
}

func (a *App) GetLobby() *LobbyInfo {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.lobby
}

func (a *App) SetLobbyID(id uint64) {
	a.mu.Lock()
	if a.lobby != nil {
		a.lobby.LobbyID = id
	}
	a.mu.Unlock()
}

func (a *App) SetLobbyStatus(s *LobbyStatus) {
	a.mu.Lock()
	a.lobbyStatus = s
	a.mu.Unlock()
}

func (a *App) GetLobbyStatus() *LobbyStatus {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.lobbyStatus
}
