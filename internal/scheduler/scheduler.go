package scheduler

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/benchlib/agent/internal/config"
	"github.com/benchlib/agent/internal/connector/navidrome"
	"github.com/benchlib/agent/internal/connector/plex"
	"github.com/benchlib/agent/internal/payload"
	"github.com/benchlib/agent/internal/sender"
)

// ScanResult — résultat d'un envoi pour une bibliothèque
type ScanResult struct {
	ConnectorName string
	LibraryName   string
	MediaType     string
	ServiceType   string
	Score         float64
	Certification string
	TotalItems    int
	DurationMs    int
	Attempts      int
	Error         string
	UserError     string
	DebugError    string
	Timestamp     time.Time
	PayloadJSON   string
	IsRemote      bool
}

// Scheduler gère le cron et l'historique des scans
type Scheduler struct {
	mu        sync.RWMutex
	cron      *cron.Cron
	history   []ScanResult
	maxHist   int
	getCfg    func() *config.Config
	isRunning bool
}

func New(getCfg func() *config.Config) *Scheduler {
	return &Scheduler{
		getCfg:  getCfg,
		maxHist: 100,
		cron:    cron.New(),
	}
}

// LoadRemoteHistory charge l'historique depuis l'API BenchLib au démarrage
func (s *Scheduler) LoadRemoteHistory(apiURL, apiKey string) {
	if apiKey == "" {
		return
	}
	url := strings.TrimRight(apiURL, "/") + "/api/v1/history"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		log.Printf("[scheduler] LoadRemoteHistory: %v", err)
		return
	}
	req.Header.Set("X-BenchLib-Key", apiKey)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[scheduler] LoadRemoteHistory: impossible de contacter BenchLib: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		log.Printf("[scheduler] LoadRemoteHistory: statut %d", resp.StatusCode)
		return
	}

	var body struct {
		Success bool `json:"success"`
		History []struct {
			ServiceType   string     `json:"serviceType"`
			MediaType     string     `json:"mediaType"`
			Score         float64    `json:"score"`
			Certification string     `json:"certification"`
			TotalItems    int        `json:"totalItems"`
			ScannedAt     *time.Time `json:"scannedAt"`
		} `json:"history"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		log.Printf("[scheduler] LoadRemoteHistory: décodage: %v", err)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, h := range body.History {
		ts := time.Now()
		if h.ScannedAt != nil {
			ts = *h.ScannedAt
		}
		s.history = append(s.history, ScanResult{
			ServiceType:   h.ServiceType,
			MediaType:     h.MediaType,
			Score:         h.Score,
			Certification: h.Certification,
			TotalItems:    h.TotalItems,
			Timestamp:     ts,
			IsRemote:      true,
		})
	}
	log.Printf("[scheduler] historique chargé depuis BenchLib — %d entrées", len(body.History))
}

func (s *Scheduler) Start() error {
	cfg := s.getCfg()
	expr := fmt.Sprintf("%d %d * * *", cfg.Schedule.Minute, cfg.Schedule.Hour)

	_, err := s.cron.AddFunc(expr, func() {
		s.RunNow()
	})
	if err != nil {
		return fmt.Errorf("cron invalide (%s): %v", expr, err)
	}
	s.cron.Start()
	log.Printf("[scheduler] démarré — scan planifié à %02d:%02d chaque jour", cfg.Schedule.Hour, cfg.Schedule.Minute)
	return nil
}

func (s *Scheduler) Stop() {
	s.cron.Stop()
}

// Restart recharge la config et recrée le cron (utilisé après changement d'heure)
func (s *Scheduler) Restart() error {
	s.cron.Stop()
	s.cron = cron.New()
	return s.Start()
}

// RunNow déclenche un scan immédiat (toutes les bibliothèques activées)
func (s *Scheduler) RunNow() {
	s.mu.Lock()
	if s.isRunning {
		s.mu.Unlock()
		log.Println("[scheduler] scan déjà en cours, ignoré")
		return
	}
	s.isRunning = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.isRunning = false
		s.mu.Unlock()
	}()

	cfg := s.getCfg()
	log.Println("[scheduler] début du scan")

	for _, conn := range cfg.Connectors {
		grouped := map[string][]config.LibraryConfig{}
		for _, lib := range conn.Libraries {
			if !lib.Enabled {
				continue
			}
			grouped[lib.MediaType] = append(grouped[lib.MediaType], lib)
		}

		for mediaType, libs := range grouped {
			var result ScanResult
			if len(libs) == 1 {
				result = s.scanLibrary(cfg, conn, libs[0])
			} else {
				result = s.scanAndMergeLibraries(cfg, conn, mediaType, libs)
			}
			s.pushHistory(result)

			if result.Error != "" {
				log.Printf("[scheduler] %-20s %-15s ERROR: %s", conn.Name, mediaType, result.Error)
			} else {
				log.Printf("[scheduler] %-20s %-15s score=%.1f cert=%s items=%d",
					conn.Name, mediaType, result.Score, result.Certification, result.TotalItems)
			}
		}
	}
	log.Println("[scheduler] scan terminé")
}

func (s *Scheduler) scanLibrary(cfg *config.Config, conn config.ConnectorConfig, lib config.LibraryConfig) ScanResult {
	base := ScanResult{
		ConnectorName: conn.Name,
		LibraryName:   lib.Name,
		MediaType:     lib.MediaType,
		Timestamp:     time.Now(),
	}

	var p *payload.IngestPayload
	var err error

	switch conn.Type {
	case config.ConnectorPlex:
		c := plex.New(conn)
		base.ServiceType = "PLEX"
		// apiURL et apiKey transmis pour /api/v1/series/reference (completenessRatio)
		p, err = c.BuildPayload(lib, conn.PublicURL, cfg.BenchlibAPIURL, cfg.BenchlibAPIKey)
	case config.ConnectorNavidrome:
		c := navidrome.New(conn)
		base.ServiceType = "NAVIDROME"
		p, err = c.BuildPayload(lib, conn.PublicURL)
	default:
		base.Error = fmt.Sprintf("connecteur inconnu: %s", conn.Type)
		return base
	}

	if err != nil {
		base.Error = err.Error()
		return base
	}

	base.TotalItems = p.Stats.TotalItems

	if pJSON, err := json.Marshal(p); err == nil {
		base.PayloadJSON = string(pJSON)
	}

	res := sender.Send(cfg.BenchlibAPIURL, cfg.BenchlibAPIKey, p)
	base.Score = res.Score
	base.Certification = res.Certification
	base.DurationMs = res.DurationMs
	base.Attempts = res.Attempts
	base.Error = res.Error
	base.UserError = res.UserError
	base.DebugError = res.DebugError
	return base
}

// scanAndMergeLibraries scanne plusieurs bibliothèques du même mediaType
// et fusionne leurs stats en un seul payload avant envoi
func (s *Scheduler) scanAndMergeLibraries(cfg *config.Config, conn config.ConnectorConfig, mediaType string, libs []config.LibraryConfig) ScanResult {
	base := ScanResult{
		ConnectorName: conn.Name,
		LibraryName:   mediaType,
		MediaType:     mediaType,
		Timestamp:     time.Now(),
	}

	names := make([]string, len(libs))
	for i, l := range libs {
		names[i] = l.Name
	}
	log.Printf("[scheduler] fusion %s : %v", mediaType, names)

	var merged *payload.IngestPayload
	for _, lib := range libs {
		var p *payload.IngestPayload
		var err error

		switch conn.Type {
		case config.ConnectorPlex:
			c := plex.New(conn)
			base.ServiceType = "PLEX"
			p, err = c.BuildPayload(lib, conn.PublicURL, cfg.BenchlibAPIURL, cfg.BenchlibAPIKey)
		case config.ConnectorNavidrome:
			c := navidrome.New(conn)
			base.ServiceType = "NAVIDROME"
			p, err = c.BuildPayload(lib, conn.PublicURL)
		default:
			base.Error = fmt.Sprintf("connecteur inconnu: %s", conn.Type)
			return base
		}

		if err != nil {
			log.Printf("[scheduler] fusion — erreur sur %s: %v", lib.Name, err)
			continue
		}

		if merged == nil {
			merged = p
		} else {
			mergeStats(&merged.Stats, &p.Stats)
		}
	}

	if merged == nil {
		base.Error = "toutes les bibliothèques sont vides ou en erreur"
		return base
	}

	base.TotalItems = merged.Stats.TotalItems

	if pJSON, err := json.Marshal(merged); err == nil {
		base.PayloadJSON = string(pJSON)
	}

	res := sender.Send(cfg.BenchlibAPIURL, cfg.BenchlibAPIKey, merged)
	base.Score = res.Score
	base.Certification = res.Certification
	base.DurationMs = res.DurationMs
	base.Attempts = res.Attempts
	base.Error = res.Error
	base.UserError = res.UserError
	base.DebugError = res.DebugError
	return base
}

// mergeStats additionne les statistiques de deux payloads
func mergeStats(dst, src *payload.Stats) {
	dst.TotalItems += src.TotalItems
	dst.Items4kDv += src.Items4kDv
	dst.Items4kHdr += src.Items4kHdr
	dst.Items4k += src.Items4k
	dst.Items1080p += src.Items1080p
	dst.Items720p += src.Items720p
	dst.ItemsSd += src.ItemsSd
	dst.ItemsAtmos += src.ItemsAtmos
	dst.ItemsTrueHd += src.ItemsTrueHd
	dst.ItemsDtsHd += src.ItemsDtsHd
	dst.ItemsDts += src.ItemsDts
	dst.ItemsAc3 += src.ItemsAc3
	dst.ItemsStereo += src.ItemsStereo
	dst.ItemsFlac += src.ItemsFlac
	dst.ItemsMp3320 += src.ItemsMp3320
	dst.ItemsAddedLast30Days += src.ItemsAddedLast30Days
	dst.ItemsAddedLast7Days += src.ItemsAddedLast7Days
	dst.SubtitledItemCount += src.SubtitledItemCount
	dst.TotalFileSizeBytes += src.TotalFileSizeBytes

	// Durée moyenne pondérée
	if src.TotalItems > 0 && dst.TotalItems > 0 {
		total := dst.TotalItems
		dst.AverageDurationSeconds = (dst.AverageDurationSeconds*(total-src.TotalItems) + src.AverageDurationSeconds*src.TotalItems) / total
	}

	// Dernier ajout — garder le plus récent
	if src.LastAddedAt != nil {
		if dst.LastAddedAt == nil || src.LastAddedAt.After(*dst.LastAddedAt) {
			dst.LastAddedAt = src.LastAddedAt
		}
	}

	// completenessRatio — moyenne pondérée par TotalItems
	// Si une seule bibliothèque l'a, on le conserve directement.
	// Si les deux l'ont, on pondère par nombre d'items (approximation).
	if src.CompletenessRatio != nil {
		if dst.CompletenessRatio == nil {
			dst.CompletenessRatio = src.CompletenessRatio
		} else {
			dstItems := float64(dst.TotalItems - src.TotalItems)
			srcItems := float64(src.TotalItems)
			total := dstItems + srcItems
			if total > 0 {
				merged := (*dst.CompletenessRatio*dstItems + *src.CompletenessRatio*srcItems) / total
				dst.CompletenessRatio = &merged
			}
		}
	}

	// Fusionner les maps
	for k, v := range src.ResolutionBreakdown {
		dst.ResolutionBreakdown[k] += v
	}
	for k, v := range src.AudioCodecBreakdown {
		dst.AudioCodecBreakdown[k] += v
	}
	for k, v := range src.AudioLanguageBreakdown {
		dst.AudioLanguageBreakdown[k] += v
	}
	for k, v := range src.SubtitleLanguageBreakdown {
		dst.SubtitleLanguageBreakdown[k] += v
	}
	for k, v := range src.DecadeBreakdown {
		dst.DecadeBreakdown[k] += v
	}
	for k, v := range src.YearBreakdown {
		dst.YearBreakdown[k] += v
	}
}

func (s *Scheduler) pushHistory(r ScanResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history = append([]ScanResult{r}, s.history...)
	if len(s.history) > s.maxHist {
		s.history = s.history[:s.maxHist]
	}
}

// IsRunning retourne true si un scan est en cours
func (s *Scheduler) IsRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.isRunning
}

// History retourne une copie de l'historique (thread-safe)
func (s *Scheduler) History() []ScanResult {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp := make([]ScanResult, len(s.history))
	copy(cp, s.history)
	return cp
}

// NextRun retourne la prochaine exécution planifiée
func (s *Scheduler) NextRun() *time.Time {
	entries := s.cron.Entries()
	if len(entries) == 0 {
		return nil
	}
	t := entries[0].Next
	return &t
}