package app

import "sync"

type LobbyInfo struct {
	Name     string `json:"name"`
	Password string `json:"password"`
	Preset   string `json:"preset"`
}

// App holds shared runtime state accessible by all layers.
type App struct {
	mu      sync.RWMutex
	gcReady bool
	lobby   *LobbyInfo
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
	a.mu.Unlock()
}

func (a *App) GetLobby() *LobbyInfo {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.lobby
}
