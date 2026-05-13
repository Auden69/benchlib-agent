//go:build windows

package main

import (
	"flag"
	"fmt"
	"hash/fnv"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/benchlib/agent/internal/config"
	"github.com/benchlib/agent/internal/scheduler"
	"github.com/benchlib/agent/internal/web"
	"github.com/getlantern/systray"
	"golang.org/x/sys/windows/registry"
)

const (
	appName       = "BenchLib Agent"
	regRunKey     = `Software\Microsoft\Windows\CurrentVersion\Run`
	defaultAPIURL = "https://api.benchlib.com"
)

func apiKeyJitter(apiKey string) int {
	h := fnv.New32a()
	h.Write([]byte(apiKey))
	return int(h.Sum32() % 60)
}

func main() {
	cfgPath := flag.String("config", defaultConfigPath(), "chemin vers config.yaml")
	apiURL := flag.String("api-url", "", "URL de l'API BenchLib (override, dev uniquement)")
	flag.Parse()

	// ── Chargement config ────────────────────────────────────────────────────
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("[tray] impossible de charger la config : %v", err)
	}

	if *apiURL != "" {
		cfg.BenchlibAPIURL = *apiURL
	} else if envURL := os.Getenv("BENCHLIB_API_URL"); envURL != "" {
		cfg.BenchlibAPIURL = envURL
	} else {
		cfg.BenchlibAPIURL = defaultAPIURL
	}

	// ── Config partagée (même pattern que cmd/agent) ─────────────────────────
	var mu sync.RWMutex
	getCfg := func() *config.Config {
		mu.RLock()
		defer mu.RUnlock()
		return cfg
	}
	saveCfg := func(newCfg *config.Config) error {
		mu.Lock()
		defer mu.Unlock()
		cfg = newCfg
		return config.Save(newCfg)
	}

	// ── Scheduler ────────────────────────────────────────────────────────────
	sched := scheduler.New(getCfg)
	if err := sched.Start(); err != nil {
		log.Fatalf("[tray] scheduler : %v", err)
	}
	defer sched.Stop()

	if cfg.BenchlibAPIKey != "" {
		jitter := apiKeyJitter(cfg.BenchlibAPIKey)
		originalMinute := cfg.Schedule.Minute
		cfg.Schedule.Minute = (cfg.Schedule.Minute + jitter) % 60
		if cfg.Schedule.Minute < originalMinute {
			cfg.Schedule.Hour = (cfg.Schedule.Hour + 1) % 24
		}
		log.Printf("[tray] scan planifié à %02d:%02d (jitter +%dmin)",
			cfg.Schedule.Hour, cfg.Schedule.Minute, jitter)
	}

	go sched.LoadRemoteHistory(cfg.BenchlibAPIURL, cfg.BenchlibAPIKey)

	// ── Serveur web (goroutine) ───────────────────────────────────────────────
	srv := web.NewServer(getCfg, saveCfg, sched)
	go func() {
		if err := srv.Start(); err != nil {
			log.Fatalf("[tray] serveur web : %v", err)
		}
	}()

	// ── Démarrage automatique Windows ────────────────────────────────────────
	ensureAutostart()

	// ── Systray ──────────────────────────────────────────────────────────────
	systray.Run(onReady(getCfg), onExit)
}

func onReady(getCfg func() *config.Config) func() {
	return func() {
		systray.SetIcon(iconData)
		systray.SetTitle(appName)
		systray.SetTooltip(appName + " — en cours d'exécution")

		mOpen := systray.AddMenuItem("Ouvrir l'interface", "Ouvre http://localhost:PORT dans le navigateur")
		systray.AddSeparator()
		mStatus := systray.AddMenuItem("Statut : actif ✓", "")
		mStatus.Disable()
		systray.AddSeparator()
		mQuit := systray.AddMenuItem("Quitter", "Arrête l'agent")

		go func() {
			for {
				select {
				case <-mOpen.ClickedCh:
					cfg := getCfg()
					port := cfg.Port
					if port == 0 {
						port = 8090
					}
					url := fmt.Sprintf("http://localhost:%d", port)
					openBrowser(url)
				case <-mQuit.ClickedCh:
					systray.Quit()
				}
			}
		}()
	}
}

func onExit() {
	log.Println("[tray] arrêt propre")
}

// openBrowser ouvre l'URL dans le navigateur par défaut (Windows).
func openBrowser(url string) {
	exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

// defaultConfigPath retourne %APPDATA%\BenchLib\config.yaml sur Windows.
func defaultConfigPath() string {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return "config.yaml"
	}
	dir := filepath.Join(appData, "BenchLib")
	os.MkdirAll(dir, 0755)
	return filepath.Join(dir, "config.yaml")
}

// ensureAutostart ajoute l'exe au démarrage Windows via le registre.
func ensureAutostart() {
	exe, err := os.Executable()
	if err != nil {
		log.Printf("[tray] autostart : impossible de récupérer le chemin exe : %v", err)
		return
	}

	k, err := registry.OpenKey(registry.CURRENT_USER, regRunKey, registry.SET_VALUE)
	if err != nil {
		log.Printf("[tray] autostart : impossible d'ouvrir le registre : %v", err)
		return
	}
	defer k.Close()

	if err := k.SetStringValue(appName, exe); err != nil {
		log.Printf("[tray] autostart : impossible d'écrire dans le registre : %v", err)
		return
	}
	log.Printf("[tray] autostart enregistré : %s", exe)
}
