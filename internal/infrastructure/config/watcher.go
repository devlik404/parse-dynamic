package config

import (
	"log"
	"path/filepath"

	"github.com/fsnotify/fsnotify"
)

/*
WatchEnv
--------
Monitor perubahan file .env
Jika berubah → reload config → update manager
*/

func WatchEnv(path string, manager *Manager) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Println("watcher error:", err)
		return
	}

	go func() {
		defer watcher.Close()

		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}

				// jika file .env diubah
				if event.Op&(fsnotify.Write|fsnotify.Create) != 0 {
					if filepath.Base(event.Name) == filepath.Base(path) {
						cfg, err := LoadConfig()
						if err != nil {
							log.Println("[CONFIG] reload failed:", err)
							continue
						}
						manager.Update(cfg)
					}
				}

			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Println("watcher error:", err)
			}
		}
	}()

	// watch directory, bukan file langsung
	dir := filepath.Dir(path)
	if err := watcher.Add(dir); err != nil {
		log.Println("watcher add error:", err)
	}
}
