package plex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/benchlib/agent/internal/config"
	"github.com/benchlib/agent/internal/payload"
)

const (
	pageSize     = 500
	httpTimeout  = 30 * time.Second
	agentVersion = "1.0.0"
)

type Connector struct {
	cfg    config.ConnectorConfig
	client *http.Client
}

func New(cfg config.ConnectorConfig) *Connector {
	return &Connector{
		cfg:    cfg,
		client: &http.Client{Timeout: httpTimeout},
	}
}

// ─── Auth headers ────────────────────────────────────────────────────────────

func (c *Connector) headers() map[string]string {
	return map[string]string{
		"X-Plex-Token":             c.cfg.Token,
		"X-Plex-Client-Identifier": "benchlib-agent",
		"X-Plex-Product":           "BenchLib Agent",
		"Accept":                   "application/json",
	}
}

func (c *Connector) get(path string) ([]byte, int, error) {
	req, err := http.NewRequest("GET", c.cfg.URL+path, nil)
	if err != nil {
		return nil, 0, err
	}
	for k, v := range c.headers() {
		req.Header.Set(k, v)
	}
	resp, err := c.client.Do(req)
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
	body, status, err := c.get("/identity")
	if err != nil {
		return TestResult{Error: fmt.Sprintf("impossible de contacter Plex : %v", err)}
	}
	if status != 200 {
		return TestResult{Error: fmt.Sprintf("Plex a répondu %d", status)}
	}
	ms := int(time.Since(start).Milliseconds())

	var data struct {
		MediaContainer struct {
			FriendlyName string `json:"friendlyName"`
			Version      string `json:"version"`
		} `json:"MediaContainer"`
	}
	_ = json.Unmarshal(body, &data)
	return TestResult{
		Success:    true,
		LatencyMs:  ms,
		ServerName: orDefault(data.MediaContainer.FriendlyName, "Plex Media Server"),
		Version:    orDefault(data.MediaContainer.Version, "unknown"),
	}
}

// ─── Libraries ───────────────────────────────────────────────────────────────

type Library struct {
	ID        string
	Title     string
	Type      string // movie, show, artist
	ItemCount int
}

func (c *Connector) GetLibraries() ([]Library, error) {
	body, status, err := c.get("/library/sections")
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("plex /library/sections a répondu %d", status)
	}

	var data struct {
		MediaContainer struct {
			Directory []struct {
				Key   string `json:"key"`
				Title string `json:"title"`
				Type  string `json:"type"`
				Count int    `json:"count"`
			} `json:"Directory"`
		} `json:"MediaContainer"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}

	libs := make([]Library, 0, len(data.MediaContainer.Directory))
	for _, d := range data.MediaContainer.Directory {
		if d.Type == "photo" {
			continue
		}
		libs = append(libs, Library{
			ID:        d.Key,
			Title:     d.Title,
			Type:      d.Type,
			ItemCount: d.Count,
		})
	}
	return libs, nil
}

// ─── Items (paginated) ───────────────────────────────────────────────────────

type stream struct {
	StreamType   int    `json:"streamType"`
	Codec        string `json:"codec"`
	Channels     int    `json:"channels"`
	Language     string `json:"language"`
	DisplayTitle string `json:"displayTitle"`
}

type plexMedia struct {
	VideoResolution string `json:"videoResolution"`
	AudioCodec      string `json:"audioCodec"`
	AudioChannels   int    `json:"audioChannels"`
	Part            []struct {
		Size   int64    `json:"size"`
		Stream []stream `json:"Stream"`
	} `json:"Part"`
}

type plexItem struct {
	RatingKey string      `json:"ratingKey"`
	Title     string      `json:"title"`
	Year      int         `json:"year"`
	Duration  int64       `json:"duration"` // ms
	AddedAt   int64       `json:"addedAt"`  // unix
	Media     []plexMedia `json:"Media"`
}

type item struct {
	videoResolution   string
	videoDisplayTitle string
	audioCodec        string
	audioChannels     int
	audioLanguages    []string
	hasSubtitles      bool
	subtitleLangs     []string
	addedAt           time.Time
	year              int
	fileSize          int64
	durationSec       int
}

// ─── Priorité codec audio ────────────────────────────────────────────────────

var audioPriority = []string{
	"atmos",
	"truehd",
	"dts-hd", "dtshd", "dts-x", "dtsx",
	"dts",
	"eac3", "ac3", "dd",
	"flac",
	"mp3",
	"aac", "stereo", "pcm",
}

func bestAudioCodec(current, candidate string) string {
	if current == "" {
		return candidate
	}
	if candidate == "" {
		return current
	}
	for _, p := range audioPriority {
		if strings.Contains(current, p) {
			return current
		}
		if strings.Contains(candidate, p) {
			return candidate
		}
	}
	return current
}

func (c *Connector) fetchPage(libraryID string, offset int, libType string) ([]byte, error) {
	typeParam := ""
	switch libType {
	case "show":
		typeParam = "&type=4" // épisodes
	case "artist":
		typeParam = "&type=10" // tracks
	}

	candidates := []string{"&includeElements=Stream", "&includeStreams=1", ""}
	for _, param := range candidates {
		path := fmt.Sprintf(
			"/library/sections/%s/all?X-Plex-Container-Start=%d&X-Plex-Container-Size=%d%s%s",
			libraryID, offset, pageSize, typeParam, param,
		)
		body, status, err := c.get(path)
		if err != nil {
			return nil, err
		}
		if status == 200 {
			return body, nil
		}
		if status != 500 {
			return nil, fmt.Errorf("plex a répondu %d", status)
		}
	}
	return nil, fmt.Errorf("plex /library/sections/%s/all inaccessible", libraryID)
}

func (c *Connector) getItems(libraryID, libType string) ([]item, int, error) {
	var items []item
	offset := 0
	totalSize := -1
	apiMs := 0

	for {
		start := time.Now()
		body, err := c.fetchPage(libraryID, offset, libType)
		if err != nil {
			return nil, 0, err
		}
		if offset == 0 {
			apiMs = int(time.Since(start).Milliseconds())
		}

		var data struct {
			MediaContainer struct {
				TotalSize int        `json:"totalSize"`
				Size      int        `json:"size"`
				Metadata  []plexItem `json:"Metadata"`
			} `json:"MediaContainer"`
		}
		if err := json.Unmarshal(body, &data); err != nil {
			return nil, 0, err
		}

		if totalSize < 0 {
			totalSize = data.MediaContainer.TotalSize
			if totalSize == 0 {
				totalSize = data.MediaContainer.Size
			}
		}

		for _, raw := range data.MediaContainer.Metadata {
			items = append(items, mapItem(raw))
		}

		offset += len(data.MediaContainer.Metadata)
		if len(data.MediaContainer.Metadata) == 0 || offset >= totalSize {
			break
		}
	}
	return items, apiMs, nil
}

func mapItem(raw plexItem) item {
	it := item{year: raw.Year}
	if raw.AddedAt > 0 {
		it.addedAt = time.Unix(raw.AddedAt, 0)
	}
	if raw.Duration > 0 {
		it.durationSec = int(raw.Duration / 1000)
	}

	if len(raw.Media) == 0 {
		return it
	}
	media := raw.Media[0]
	it.videoResolution = mapResolution(media.VideoResolution)
	it.audioCodec = strings.ToLower(media.AudioCodec)
	it.audioChannels = media.AudioChannels
	it.fileSize = 0

	if len(media.Part) > 0 {
		part := media.Part[0]
		it.fileSize = part.Size

		seen := map[string]bool{}
		seenSub := map[string]bool{}
		var displayTitle string

		for _, s := range part.Stream {
			switch s.StreamType {
			case 1: // video
				if s.DisplayTitle != "" {
					displayTitle = s.DisplayTitle
				}
			case 2: // audio
				if s.Language != "" && !seen[s.Language] {
					it.audioLanguages = append(it.audioLanguages, s.Language)
					seen[s.Language] = true
				}
				if s.Codec != "" {
					it.audioCodec = bestAudioCodec(it.audioCodec, strings.ToLower(s.Codec))
				}
				if s.Channels > it.audioChannels {
					it.audioChannels = s.Channels
				}
			case 3: // subtitle
				it.hasSubtitles = true
				if s.Language != "" && !seenSub[s.Language] {
					it.subtitleLangs = append(it.subtitleLangs, s.Language)
					seenSub[s.Language] = true
				}
			}
		}
		it.videoDisplayTitle = displayTitle
	}
	return it
}

func mapResolution(raw string) string {
	r := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case r == "4k" || r == "2160" || strings.HasPrefix(r, "4k") || strings.Contains(r, "2160"):
		return "4k"
	case r == "1080" || strings.Contains(r, "1080"):
		return "1080p"
	case r == "720" || strings.Contains(r, "720"):
		return "720p"
	case r == "480" || strings.Contains(r, "480"):
		return "480p"
	case r == "sd" || r == "576" || strings.HasPrefix(r, "sd"):
		return "sd"
	default:
		return "unknown"
	}
}

// ─── Artist / Album counts ───────────────────────────────────────────────────

func (c *Connector) getCount(libraryID, itemType string) (int, error) {
	path := fmt.Sprintf(
		"/library/sections/%s/all?X-Plex-Container-Start=0&X-Plex-Container-Size=0&type=%s",
		libraryID, itemType,
	)
	body, status, err := c.get(path)
	if err != nil {
		return 0, err
	}
	if status != 200 {
		return 0, fmt.Errorf("plex a répondu %d", status)
	}
	var data struct {
		MediaContainer struct {
			TotalSize int `json:"totalSize"`
			Size      int `json:"size"`
		} `json:"MediaContainer"`
	}
	_ = json.Unmarshal(body, &data)
	if data.MediaContainer.TotalSize > 0 {
		return data.MediaContainer.TotalSize, nil
	}
	return data.MediaContainer.Size, nil
}

// ─── Séries — GUIDs et épisodes présents ────────────────────────────────────
// Récupère les shows (type=2) de la bibliothèque.
//
// L'API Plex retourne deux champs GUID :
//   - "guid" (racine)  : souvent un format agent legacy ex: "com.plexapp.agents.thetvdb://..."
//   - "Guid" (tableau) : liste des identifiants natifs dont "plex://show/..."
//
// On lit le tableau "Guid" en priorité pour extraire le GUID natif Plex,
// qui correspond au format stocké dans SeriesReference.plexGuid en base.
// Si le tableau est absent (ancienne API), on tombe sur "guid" racine.

type showEntry struct {
	PlexGUID        string
	EpisodesPresent int
}

type plexGuidEntry struct {
	ID string `json:"id"`
}

type plexShow struct {
	GUID      string          `json:"guid"`       // format legacy potentiel
	GuidList  []plexGuidEntry `json:"Guid"`       // tableau natif — priorité
	LeafCount int             `json:"leafCount"`
	Title     string          `json:"title"`
}

// extractPlexGUID retourne le GUID natif Plex (plex://show/...) depuis un show.
// Priorité : tableau Guid[] → champ guid racine.
func extractPlexGUID(s plexShow) string {
	// Chercher dans le tableau Guid[] un identifiant natif Plex
	for _, g := range s.GuidList {
		if strings.HasPrefix(g.ID, "plex://show/") {
			return g.ID
		}
	}
	// Fallback : champ guid racine
	if s.GUID != "" {
		return s.GUID
	}
	return ""
}

func (c *Connector) getShows(libraryID string) ([]showEntry, error) {
	var shows []showEntry
	offset := 0
	totalSize := -1

	for {
		path := fmt.Sprintf(
			"/library/sections/%s/all?X-Plex-Container-Start=%d&X-Plex-Container-Size=%d&type=2",
			libraryID, offset, pageSize,
		)
		body, status, err := c.get(path)
		if err != nil {
			return nil, err
		}
		if status != 200 {
			return nil, fmt.Errorf("plex /library/sections/%s/all?type=2 a répondu %d", libraryID, status)
		}

		var data struct {
			MediaContainer struct {
				TotalSize int        `json:"totalSize"`
				Size      int        `json:"size"`
				Metadata  []plexShow `json:"Metadata"`
			} `json:"MediaContainer"`
		}
		if err := json.Unmarshal(body, &data); err != nil {
			return nil, err
		}

		if totalSize < 0 {
			totalSize = data.MediaContainer.TotalSize
			if totalSize == 0 {
				totalSize = data.MediaContainer.Size
			}
		}

		for _, s := range data.MediaContainer.Metadata {
			guid := extractPlexGUID(s)
			if guid != "" {
				shows = append(shows, showEntry{
					PlexGUID:        guid,
					EpisodesPresent: s.LeafCount,
				})
			}
		}

		offset += len(data.MediaContainer.Metadata)
		if len(data.MediaContainer.Metadata) == 0 || offset >= totalSize {
			break
		}
	}

	fmt.Printf("[plex] getShows — %d shows récupérés (lib %s)\n", len(shows), libraryID)
	if len(shows) > 0 {
		// Log des 3 premiers pour debug
		for i, s := range shows {
			if i >= 3 {
				break
			}
			fmt.Printf("[plex]   show[%d] guid=%s episodes=%d\n", i, s.PlexGUID, s.EpisodesPresent)
		}
	}

	return shows, nil
}

// ─── BenchLib — récupération des références TMDB ────────────────────────────

type seriesRefEntry struct {
	GUID          string `json:"guid"`
	TotalEpisodes *int   `json:"totalEpisodes"`
	IsResolved    bool   `json:"isResolved"`
}

type seriesRefResponse struct {
	References []seriesRefEntry `json:"references"`
}

func fetchSeriesReferences(apiURL, apiKey string, guids []string) (map[string]int, error) {
	if len(guids) == 0 {
		return map[string]int{}, nil
	}

	body, err := json.Marshal(map[string]interface{}{"guids": guids})
	if err != nil {
		return nil, fmt.Errorf("sérialisation GUIDs : %w", err)
	}

	req, err := http.NewRequest("POST", apiURL+"/api/v1/series/reference", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("création requête : %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-BenchLib-Key", apiKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("appel /series/reference : %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("/series/reference a répondu %d", resp.StatusCode)
	}

	var result seriesRefResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("décodage réponse : %w", err)
	}

	refMap := make(map[string]int, len(result.References))
	resolved := 0
	for _, r := range result.References {
		if r.IsResolved && r.TotalEpisodes != nil {
			refMap[r.GUID] = *r.TotalEpisodes
			resolved++
		}
	}
	fmt.Printf("[plex] fetchSeriesReferences — %d/%d séries résolues\n", resolved, len(guids))
	return refMap, nil
}

// ─── calcCompletenessRatio ───────────────────────────────────────────────────

func calcCompletenessRatio(shows []showEntry, refMap map[string]int) *float64 {
	var epPresent, epReference int

	for _, show := range shows {
		total, ok := refMap[show.PlexGUID]
		if !ok || total <= 0 {
			continue
		}
		epReference += total
		present := show.EpisodesPresent
		if present > total {
			present = total
		}
		epPresent += present
	}

	if epReference == 0 {
		return nil
	}

	ratio := float64(epPresent) / float64(epReference)
	fmt.Printf("[plex] completenessRatio = %.4f (%d/%d épisodes)\n", ratio, epPresent, epReference)
	return &ratio
}

// ─── Build payload ───────────────────────────────────────────────────────────

func (c *Connector) BuildPayload(lib config.LibraryConfig, publicURL, apiURL, apiKey string) (*payload.IngestPayload, error) {
	libType := mediaTypeToPlexType(lib.MediaType)
	items, apiMs, err := c.getItems(lib.ID, libType)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("bibliothèque vide, skip")
	}

	stats := aggregateStats(items)
	stats.TotalItems = len(items)

	if lib.MediaType == "MUSIC" {
		if n, err := c.getCount(lib.ID, "8"); err == nil {
			stats.ArtistCount = n
		}
		if n, err := c.getCount(lib.ID, "9"); err == nil {
			stats.TotalAlbums = n
		}
	}

	if lib.MediaType == "SERIES" && apiURL != "" && apiKey != "" {
		shows, err := c.getShows(lib.ID)
		if err != nil {
			fmt.Printf("[plex] getShows erreur (non bloquant) : %v\n", err)
		} else if len(shows) > 0 {
			guids := make([]string, 0, len(shows))
			for _, s := range shows {
				guids = append(guids, s.PlexGUID)
			}

			refMap, err := fetchSeriesReferences(apiURL, apiKey, guids)
			if err != nil {
				fmt.Printf("[plex] fetchSeriesReferences erreur (non bloquant) : %v\n", err)
			} else {
				stats.CompletenessRatio = calcCompletenessRatio(shows, refMap)
			}
		}
	}

	p := &payload.IngestPayload{
		AgentVersion: agentVersion,
		ServiceType:  "PLEX",
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

func aggregateStats(items []item) payload.Stats {
	stats := payload.Stats{
		ResolutionBreakdown:       map[string]int{},
		AudioCodecBreakdown:       map[string]int{},
		AudioLanguageBreakdown:    map[string]int{},
		SubtitleLanguageBreakdown: map[string]int{},
		DecadeBreakdown:           map[string]int{},
		YearBreakdown:             map[int]int{},
	}

	now := time.Now()
	thirtyDaysAgo := now.AddDate(0, 0, -30)
	sevenDaysAgo := now.AddDate(0, 0, -7)
	var totalDuration int64

	for _, it := range items {
		dv := strings.Contains(strings.ToLower(it.videoDisplayTitle), "dv") ||
			strings.Contains(strings.ToLower(it.videoDisplayTitle), "dolby vision")
		hdr := strings.Contains(strings.ToLower(it.videoDisplayTitle), "hdr")

		switch it.videoResolution {
		case "4k":
			if dv {
				stats.Items4kDv++
			} else if hdr {
				stats.Items4kHdr++
			} else {
				stats.Items4k++
			}
			stats.ResolutionBreakdown["4k"]++
		case "1080p":
			stats.Items1080p++
			stats.ResolutionBreakdown["1080p"]++
		case "720p":
			stats.Items720p++
			stats.ResolutionBreakdown["720p"]++
		case "480p":
			stats.ResolutionBreakdown["480p"]++
		case "sd":
			stats.ItemsSd++
			stats.ResolutionBreakdown["sd"]++
		default:
			stats.ResolutionBreakdown["unknown"]++
		}

		codec := it.audioCodec
		switch {
		case strings.Contains(codec, "atmos"):
			stats.ItemsAtmos++
		case strings.Contains(codec, "truehd"):
			stats.ItemsTrueHd++
		case strings.Contains(codec, "dts-hd") || strings.Contains(codec, "dtshd") ||
			strings.Contains(codec, "dts-x") || strings.Contains(codec, "dtsx"):
			stats.ItemsDtsHd++
		case strings.Contains(codec, "dts"):
			stats.ItemsDts++
		case strings.Contains(codec, "eac3") || strings.Contains(codec, "ac3") ||
			strings.Contains(codec, "dd"):
			stats.ItemsAc3++
		case strings.Contains(codec, "flac"):
			stats.ItemsFlac++
		case strings.Contains(codec, "mp3"):
			stats.ItemsMp3320++
		case strings.Contains(codec, "aac") || strings.Contains(codec, "stereo") ||
			strings.Contains(codec, "pcm"):
			stats.ItemsStereo++
		}
		if codec != "" {
			stats.AudioCodecBreakdown[codec]++
		}

		for _, lang := range it.audioLanguages {
			stats.AudioLanguageBreakdown[lang]++
		}

		if it.hasSubtitles {
			stats.SubtitledItemCount++
			for _, lang := range it.subtitleLangs {
				stats.SubtitleLanguageBreakdown[lang]++
			}
		}

		if !it.addedAt.IsZero() {
			if it.addedAt.After(thirtyDaysAgo) {
				stats.ItemsAddedLast30Days++
			}
			if it.addedAt.After(sevenDaysAgo) {
				stats.ItemsAddedLast7Days++
			}
			if stats.LastAddedAt == nil || it.addedAt.After(*stats.LastAddedAt) {
				t := it.addedAt
				stats.LastAddedAt = &t
			}
		}

		if it.year > 0 {
			stats.YearBreakdown[it.year]++
			decade := fmt.Sprintf("%ds", (it.year/10)*10)
			stats.DecadeBreakdown[decade]++
		}

		totalDuration += int64(it.durationSec)
		stats.TotalFileSizeBytes += it.fileSize
	}

	if len(items) > 0 {
		stats.AverageDurationSeconds = int(totalDuration / int64(len(items)))
	}

	return stats
}

func mediaTypeToPlexType(mediaType string) string {
	switch mediaType {
	case "SERIES":
		return "show"
	case "MUSIC":
		return "artist"
	default:
		return "movie"
	}
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}