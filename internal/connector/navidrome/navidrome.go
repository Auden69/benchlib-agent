package navidrome

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/benchlib/agent/internal/config"
	"github.com/benchlib/agent/internal/payload"
)

const agentVersion = "1.0.0"

type Connector struct {
	cfg    config.ConnectorConfig
	client *http.Client
}

func New(cfg config.ConnectorConfig) *Connector {
	return &Connector{
		cfg:    cfg,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// Navidrome implémente l'API Subsonic — authentification via paramètres URL
func (c *Connector) subsonicParams() string {
	return fmt.Sprintf(
		"u=%s&p=%s&v=1.16.1&c=benchlib-agent&f=json",
		c.cfg.Username, c.cfg.Password,
	)
}

func (c *Connector) get(path string) ([]byte, int, error) {
	url := fmt.Sprintf("%s/rest/%s?%s", c.cfg.URL, path, c.subsonicParams())
	resp, err := c.client.Get(url)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return body, resp.StatusCode, err
}

// ─── Connection test ─────────────────────────────────────────────────────────

type TestResult struct {
	Success    bool
	LatencyMs  int
	ServerName string
	Version    string
	Error      string
}

func (c *Connector) TestConnection() TestResult {
	start := time.Now()
	body, status, err := c.get("ping")
	if err != nil {
		return TestResult{Error: fmt.Sprintf("impossible de contacter Navidrome : %v", err)}
	}
	if status != 200 {
		return TestResult{Error: fmt.Sprintf("Navidrome a répondu %d", status)}
	}
	ms := int(time.Since(start).Milliseconds())

	var data struct {
		Response struct {
			Status  string `json:"status"`
			Version string `json:"version"`
		} `json:"subsonic-response"`
	}
	_ = json.Unmarshal(body, &data)
	if data.Response.Status != "ok" {
		return TestResult{Error: "Navidrome a répondu status != ok (credentials incorrects ?)"}
	}
	return TestResult{
		Success:    true,
		LatencyMs:  ms,
		ServerName: "Navidrome",
		Version:    data.Response.Version,
	}
}

// ─── Music stats ─────────────────────────────────────────────────────────────

type musicStats struct {
	ArtistCount int
	AlbumCount  int
	SongCount   int
	FlacCount   int
	Mp3Count    int
	AacCount    int
	OggCount    int
	TotalSize   int64
	AddedLast30 int
	AddedLast7  int
	LastAddedAt *time.Time
	Genres      map[string]int
	Decades     map[string]int
}

func (c *Connector) getMusicStats() (*musicStats, int, error) {
	// 1. Statistiques globales
	body, _, err := c.get("getMusicFolders")
	if err != nil {
		return nil, 0, err
	}
	_ = body // on n'a pas besoin des folders

	// 2. Artistes
	start := time.Now()
	aBody, _, err := c.get("getArtists")
	if err != nil {
		return nil, 0, err
	}
	apiMs := int(time.Since(start).Milliseconds())

	var artistsData struct {
		Response struct {
			Artists struct {
				Index []struct {
					Artist []struct {
						ID         string `json:"id"`
						AlbumCount int    `json:"albumCount"`
					} `json:"artist"`
				} `json:"index"`
			} `json:"artists"`
		} `json:"subsonic-response"`
	}
	_ = json.Unmarshal(aBody, &artistsData)

	stats := &musicStats{
		Genres:  map[string]int{},
		Decades: map[string]int{},
	}
	albumIDs := []string{}
	for _, idx := range artistsData.Response.Artists.Index {
		stats.ArtistCount += len(idx.Artist)
		for _, a := range idx.Artist {
			stats.AlbumCount += a.AlbumCount
			_ = a.ID
		}
	}

	// 3. Albums pour genres et décennies
	alBody, _, err := c.get("getAlbumList2&type=alphabeticalByName&size=500&offset=0")
	if err != nil {
		return stats, apiMs, nil // non bloquant
	}
	var albumList struct {
		Response struct {
			AlbumList2 struct {
				Album []struct {
					ID    string `json:"id"`
					Year  int    `json:"year"`
					Genre string `json:"genre"`
				} `json:"album"`
			} `json:"albumList2"`
		} `json:"subsonic-response"`
	}
	_ = json.Unmarshal(alBody, &albumList)
	for _, al := range albumList.Response.AlbumList2.Album {
		albumIDs = append(albumIDs, al.ID)
		if al.Genre != "" {
			stats.Genres[al.Genre]++
		}
		if al.Year > 0 {
			decade := fmt.Sprintf("%ds", (al.Year/10)*10)
			stats.Decades[decade]++
		}
	}

	// 4. Chansons récentes pour freshness
	now := time.Now()
	rBody, _, err := c.get("getAlbumList2&type=newest&size=500&offset=0")
	if err != nil {
		return stats, apiMs, nil
	}
	var recentList struct {
		Response struct {
			AlbumList2 struct {
				Album []struct {
					Created string `json:"created"`
					SongCount int  `json:"songCount"`
				} `json:"album"`
			} `json:"albumList2"`
		} `json:"subsonic-response"`
	}
	_ = json.Unmarshal(rBody, &recentList)
	for _, al := range recentList.Response.AlbumList2.Album {
		if al.Created == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, al.Created)
		if err != nil {
			continue
		}
		stats.SongCount += al.SongCount
		if stats.LastAddedAt == nil || t.After(*stats.LastAddedAt) {
			tc := t
			stats.LastAddedAt = &tc
		}
		if t.After(now.AddDate(0, 0, -30)) {
			stats.AddedLast30++
		}
		if t.After(now.AddDate(0, 0, -7)) {
			stats.AddedLast7++
		}
	}

	return stats, apiMs, nil
}

// ─── Build payload ───────────────────────────────────────────────────────────

func (c *Connector) BuildPayload(lib config.LibraryConfig, publicURL string) (*payload.IngestPayload, error) {
	ms, apiMs, err := c.getMusicStats()
	if err != nil {
		return nil, err
	}
	if ms.SongCount == 0 && ms.AlbumCount == 0 {
		return nil, fmt.Errorf("bibliothèque vide, skip")
	}

	total := ms.SongCount
	if total == 0 {
		total = ms.AlbumCount // fallback
	}

	stats := payload.Stats{
		TotalItems:           total,
		ArtistCount:          ms.ArtistCount,
		TotalAlbums:          ms.AlbumCount,
		ItemsAddedLast30Days: ms.AddedLast30,
		ItemsAddedLast7Days:  ms.AddedLast7,
		LastAddedAt:          ms.LastAddedAt,
		DecadeBreakdown:      ms.Decades,
		AudioCodecBreakdown:  map[string]int{}, // Navidrome n'expose pas le codec par track via Subsonic
	}

	p := &payload.IngestPayload{
		AgentVersion: agentVersion,
		ServiceType:  "NAVIDROME",
		MediaType:    lib.MediaType,
		ScannedAt:    time.Now().UTC(),
		PublicURL:    publicURL,
		Stats:        stats,
	}
	if apiMs > 0 {
		p.APIResponseMs = &apiMs
	}
	return p, nil
}