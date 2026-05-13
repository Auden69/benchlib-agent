package web

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/benchlib/agent/internal/config"
	"github.com/benchlib/agent/internal/connector/navidrome"
	"github.com/benchlib/agent/internal/connector/plex"
	"github.com/benchlib/agent/internal/scheduler"
)

//go:embed static/*
var staticFiles embed.FS

type Server struct {
	port      int
	getCfg    func() *config.Config
	saveCfg   func(*config.Config) error
	sched     *scheduler.Scheduler
	mux       *http.ServeMux
}

func NewServer(getCfg func() *config.Config, saveCfg func(*config.Config) error, sched *scheduler.Scheduler) *Server {
	s := &Server{
		getCfg:  getCfg,
		saveCfg: saveCfg,
		sched:   sched,
		mux:     http.NewServeMux(),
	}
	s.registerRoutes()
	return s
}

func (s *Server) Start() error {
	cfg := s.getCfg()
	s.port = cfg.Port
	addr := fmt.Sprintf(":%d", s.port)
	log.Printf("[web] interface disponible sur http://localhost%s", addr)
	return http.ListenAndServe(addr, s.mux)
}

func (s *Server) registerRoutes() {
	// Static files
	sub, _ := fs.Sub(staticFiles, "static")
	s.mux.Handle("/", http.FileServer(http.FS(sub)))

	// API
	s.mux.HandleFunc("/api/status", s.handleStatus)
	s.mux.HandleFunc("/api/config", s.handleConfig)
	s.mux.HandleFunc("/api/history", s.handleHistory)
	s.mux.HandleFunc("/api/scan", s.handleScan)
	s.mux.HandleFunc("/api/connectors/test", s.handleConnectorTest)
	s.mux.HandleFunc("/api/connectors/", s.handleConnectorsDelete)
	s.mux.HandleFunc("/api/connectors", s.handleConnectors)
}

// ── Handlers ─────────────────────────────────────────────────────────────────

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	cfg := s.getCfg()
	connected := cfg.BenchlibAPIKey != ""
	next := s.sched.NextRun()
	var nextStr string
	if next != nil {
		nextStr = next.Format(time.RFC3339)
	}
	jsonResp(w, map[string]any{
		"connected":  connected,
		"next_run":   nextStr,
		"is_running": s.sched.IsRunning(),
	})
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		jsonResp(w, s.getCfg())
	case http.MethodPatch:
		cfg := s.getCfg()
		var patch struct {
			BenchlibAPIKey string                 `json:"benchlib_api_key"`
			Port           int                    `json:"port"`
			Schedule       *config.ScheduleConfig `json:"schedule"`
		}
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if patch.BenchlibAPIKey != "" {
			cfg.BenchlibAPIKey = patch.BenchlibAPIKey
		}

		if patch.Port > 0 {
			cfg.Port = patch.Port
		}
		if patch.Schedule != nil {
			cfg.Schedule = *patch.Schedule
		}
		if err := s.saveCfg(cfg); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		// Redémarrer le scheduler si l'heure a changé
		if patch.Schedule != nil {
			_ = s.sched.Restart()
		}
		jsonResp(w, map[string]any{"ok": true})
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	hist := s.sched.History()
	// Convertir en format JSON-friendly
	type row struct {
		ConnectorName string    `json:"connector_name"`
		LibraryName   string    `json:"library_name"`
		MediaType     string    `json:"media_type"`
		ServiceType   string    `json:"service_type"`
		Score         float64   `json:"score"`
		Certification string    `json:"certification"`
		TotalItems    int       `json:"total_items"`
		DurationMs    int       `json:"duration_ms"`
		Attempts      int       `json:"attempts"`
		Error         string    `json:"error"`
		UserError     string    `json:"user_error"`
		DebugError    string    `json:"debug_error"`
		Timestamp     time.Time `json:"timestamp"`
		PayloadJSON   string    `json:"payload_json"`
		IsRemote      bool      `json:"is_remote"`
	}
	rows := make([]row, len(hist))
	for i, h := range hist {
		rows[i] = row{
			ConnectorName: h.ConnectorName,
			LibraryName:   h.LibraryName,
			MediaType:     h.MediaType,
			ServiceType:   h.ServiceType,
			Score:         h.Score,
			Certification: h.Certification,
			TotalItems:    h.TotalItems,
			DurationMs:    h.DurationMs,
			Attempts:      h.Attempts,
			Error:         h.Error,
			UserError:     h.UserError,
			DebugError:    h.DebugError,
			Timestamp:     h.Timestamp,
			PayloadJSON:   h.PayloadJSON,
			IsRemote:      h.IsRemote,
		}
	}
	jsonResp(w, rows)
}

func (s *Server) handleScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", 405)
		return
	}
	if s.sched.IsRunning() {
		http.Error(w, `{"error":"un scan est déjà en cours"}`, 409)
		return
	}
	go s.sched.RunNow()
	jsonResp(w, map[string]any{"ok": true, "message": "scan démarré en arrière-plan"})
}

// Test de connexion + détection des bibliothèques
func (s *Server) handleConnectorTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", 405)
		return
	}
	var req struct {
		Type     string `json:"type"`
		Name     string `json:"name"`
		URL      string `json:"url"`
		Token    string `json:"token"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}

	cfg := config.ConnectorConfig{
		Type:     config.ConnectorType(req.Type),
		Name:     req.Name,
		URL:      strings.TrimRight(req.URL, "/"),
		Token:    req.Token,
		Username: req.Username,
		Password: req.Password,
	}

	switch config.ConnectorType(req.Type) {
	case config.ConnectorPlex:
		c := plex.New(cfg)
		res := c.TestConnection()
		if !res.Success {
			jsonResp(w, map[string]any{"success": false, "error": res.Error})
			return
		}
		libs, err := c.GetLibraries()
		if err != nil {
			jsonResp(w, map[string]any{"success": false, "error": err.Error()})
			return
		}
		type libOut struct {
			ID        string `json:"id"`
			Title     string `json:"title"`
			Type      string `json:"type"`
			ItemCount int    `json:"item_count"`
		}
		out := make([]libOut, len(libs))
		for i, l := range libs {
			out[i] = libOut{l.ID, l.Title, l.Type, l.ItemCount}
		}
		jsonResp(w, map[string]any{
			"success":     true,
			"server_name": res.ServerName,
			"version":     res.Version,
			"latency_ms":  res.LatencyMs,
			"libraries":   out,
		})

	case config.ConnectorNavidrome:
		c := navidrome.New(cfg)
		res := c.TestConnection()
		if !res.Success {
			jsonResp(w, map[string]any{"success": false, "error": res.Error})
			return
		}
		// Navidrome = une seule lib musique
		jsonResp(w, map[string]any{
			"success":     true,
			"server_name": res.ServerName,
			"version":     res.Version,
			"latency_ms":  res.LatencyMs,
			"libraries": []map[string]any{
				{"id": "1", "title": "Musique", "type": "artist", "item_count": 0},
			},
		})

	default:
		http.Error(w, "type de connecteur inconnu", 400)
	}
}

func (s *Server) handleConnectors(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		jsonResp(w, s.getCfg().Connectors)
	case http.MethodPost:
		var req struct {
			Type         string                 `json:"type"`
			Name         string                 `json:"name"`
			URL          string                 `json:"url"`
			PublicURL    string                 `json:"public_url"`
			Token        string                 `json:"token"`
			Username     string                 `json:"username"`
			Password     string                 `json:"password"`
			Libraries    []config.LibraryConfig `json:"libraries"`
			EditingIndex int                    `json:"editing_index"` // -1 = nouveau, >=0 = édition
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		cfg := s.getCfg()
		conn := config.ConnectorConfig{
			Type:      config.ConnectorType(req.Type),
			Name:      req.Name,
			URL:       strings.TrimRight(req.URL, "/"),
			PublicURL: req.PublicURL,
			Token:     req.Token,
			Username:  req.Username,
			Password:  req.Password,
			Libraries: req.Libraries,
		}
		if conn.Libraries == nil {
			conn.Libraries = []config.LibraryConfig{}
		}
		// Si édition d'un connecteur existant (index fourni), remplacer directement
		replaced := false
		if req.EditingIndex >= 0 && req.EditingIndex < len(cfg.Connectors) {
			cfg.Connectors[req.EditingIndex] = conn
			replaced = true
		} else {
			// Sinon, remplacer si même nom existe déjà
			for i, existing := range cfg.Connectors {
				if existing.Name == conn.Name {
					cfg.Connectors[i] = conn
					replaced = true
					break
				}
			}
		}
		if !replaced {
			cfg.Connectors = append(cfg.Connectors, conn)
		}
		if err := s.saveCfg(cfg); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		jsonResp(w, map[string]any{"ok": true, "replaced": replaced})
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func (s *Server) handleConnectorsDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "DELETE required", 405)
		return
	}
	parts := strings.Split(r.URL.Path, "/")
	idxStr := parts[len(parts)-1]
	idx, err := strconv.Atoi(idxStr)
	if err != nil {
		http.Error(w, "index invalide", 400)
		return
	}
	cfg := s.getCfg()
	if idx < 0 || idx >= len(cfg.Connectors) {
		http.Error(w, "index hors limites", 404)
		return
	}
	cfg.Connectors = append(cfg.Connectors[:idx], cfg.Connectors[idx+1:]...)
	if err := s.saveCfg(cfg); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	jsonResp(w, map[string]any{"ok": true})
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func jsonResp(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}