package main

import (
	"flag"
	"hash/fnv"
	"log"
	"os"
	"sync"

	"github.com/benchlib/agent/internal/config"
	"github.com/benchlib/agent/internal/scheduler"
	"github.com/benchlib/agent/internal/web"
)

// apiKeyJitter retourne un offset en minutes (0-59) stable pour une clé donnée
func apiKeyJitter(apiKey string) int {
	h := fnv.New32a()
	h.Write([]byte(apiKey))
	return int(h.Sum32() % 60)
}

func main() {
	cfgPath := flag.String("config", "config.yaml", "chemin vers config.yaml")
	apiURL  := flag.String("api-url", "", "URL de l'API BenchLib (override, dev uniquement)")
	flag.Parse()

	// ── Chargement de la config ─────────────────────────────────────────────
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("[main] impossible de charger la config : %v", err)
	}
	log.Printf("[main] config chargée depuis %s", *cfgPath)

	// URL API — hardcodée en prod, overridable via flag ou var env (dev uniquement)
	const defaultAPIURL = "https://api.benchlib.com"
	if *apiURL != "" {
		cfg.BenchlibAPIURL = *apiURL
		log.Printf("[main] API URL override (flag) : %s", *apiURL)
	} else if envURL := os.Getenv("BENCHLIB_API_URL"); envURL != "" {
		cfg.BenchlibAPIURL = envURL
		log.Printf("[main] API URL override (env) : %s", envURL)
	} else {
		cfg.BenchlibAPIURL = defaultAPIURL
	}

	// Config partagée entre web server et scheduler — protégée par mutex
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

	// ── Scheduler ───────────────────────────────────────────────────────────
	sched := scheduler.New(getCfg)
	if err := sched.Start(); err != nil {
		log.Fatalf("[main] scheduler : %v", err)
	}
	defer sched.Stop()

	// Jitter — décaler l'heure d'envoi de 0-59 min selon la clé API
	// Évite que tous les agents envoient exactement à la même heure
	if cfg.BenchlibAPIKey != "" {
		jitter := apiKeyJitter(cfg.BenchlibAPIKey)
		originalMinute := cfg.Schedule.Minute
		originalHour := cfg.Schedule.Hour
		cfg.Schedule.Minute = (cfg.Schedule.Minute + jitter) % 60
		// Si les minutes débordent sur l'heure suivante
		if cfg.Schedule.Minute < originalMinute {
			cfg.Schedule.Hour = (cfg.Schedule.Hour + 1) % 24
		}
		log.Printf("[main] scan planifié à %02d:%02d (jitter +%dmin par rapport à %02d:%02d)",
			cfg.Schedule.Hour, cfg.Schedule.Minute, jitter, originalHour, originalMinute)
	}

	// Charger l'historique depuis BenchLib au démarrage (non bloquant)
	go sched.LoadRemoteHistory(cfg.BenchlibAPIURL, cfg.BenchlibAPIKey)

	// ── Serveur web ─────────────────────────────────────────────────────────
	srv := web.NewServer(getCfg, saveCfg, sched)
	log.Fatal(srv.Start())
}