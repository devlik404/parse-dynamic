package config

import (
	"log"
	"sync/atomic"
)

/*
ConfigManager
-------------
- Menyimpan config aktif (atomic)
- Aman untuk concurrent access
*/

type Manager struct {
	current atomic.Value // *Config
}

// NewManager init config manager
func NewManager(cfg *Config) *Manager {
	m := &Manager{}
	m.current.Store(cfg)
	return m
}

// Get ambil config terbaru
func (m *Manager) Get() *Config {
	return m.current.Load().(*Config)
}

// Update replace config (dipanggil watcher)
func (m *Manager) Update(cfg *Config) {
	m.current.Store(cfg)
	log.Println("[CONFIG] reloaded")
}
