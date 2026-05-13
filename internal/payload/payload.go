package payload

import "time"

// IngestPayload — contrat central avec POST /api/v1/ingest
// Miroir de shared/payload.schema.json côté BenchLib
type IngestPayload struct {
	AgentVersion  string    `json:"agentVersion"`
	ServiceType   string    `json:"serviceType"`             // PLEX, JELLYFIN, NAVIDROME…
	MediaType     string    `json:"mediaType"`               // MOVIES, SERIES, MUSIC…
	ScannedAt     time.Time `json:"scannedAt"`
	APIResponseMs *int      `json:"apiResponseMs,omitempty"`
	PublicURL     string    `json:"publicUrl,omitempty"`
	Stats         Stats     `json:"stats"`
}

type Stats struct {
	TotalItems int `json:"totalItems"`

	// Vidéo
	Items4kDv  int `json:"items4kDv,omitempty"`
	Items4kHdr int `json:"items4kHdr,omitempty"`
	Items4k    int `json:"items4k,omitempty"`
	Items1080p int `json:"items1080p,omitempty"`
	Items720p  int `json:"items720p,omitempty"`
	ItemsSd    int `json:"itemsSd,omitempty"`

	// Audio
	ItemsAtmos  int `json:"itemsAtmos,omitempty"`
	ItemsTrueHd int `json:"itemsTrueHd,omitempty"`
	ItemsDtsHd  int `json:"itemsDtsHd,omitempty"`
	ItemsDts    int `json:"itemsDts,omitempty"`
	ItemsAc3    int `json:"itemsAc3,omitempty"`
	ItemsStereo int `json:"itemsStereo,omitempty"`
	ItemsFlac   int `json:"itemsFlac,omitempty"`
	ItemsMp3320 int `json:"itemsMp3320,omitempty"`

	// Ajouts
	ItemsAddedLast30Days int        `json:"itemsAddedLast30Days,omitempty"`
	ItemsAddedLast7Days  int        `json:"itemsAddedLast7Days,omitempty"`
	LastAddedAt          *time.Time `json:"lastAddedAt,omitempty"`

	// Sous-titres
	SubtitledItemCount int `json:"subtitledItemCount,omitempty"`

	// Distributions
	ResolutionBreakdown       map[string]int `json:"resolutionBreakdown,omitempty"`
	AudioCodecBreakdown       map[string]int `json:"audioCodecBreakdown,omitempty"`
	AudioLanguageBreakdown    map[string]int `json:"audioLanguageBreakdown,omitempty"`
	SubtitleLanguageBreakdown map[string]int `json:"subtitleLanguageBreakdown,omitempty"`
	DecadeBreakdown           map[string]int `json:"decadeBreakdown,omitempty"`
	YearBreakdown             map[int]int    `json:"yearBreakdown,omitempty"`

	// Musique
	ArtistCount int `json:"artistCount,omitempty"`
	TotalAlbums int `json:"totalAlbums,omitempty"`

	// Séries — complétude calculée localement via /api/v1/series/reference
	// nil = BenchLib n'a pas encore résolu les séries via TMDB
	// 0.0-1.0 = ratio épisodes présents / épisodes de référence
	CompletenessRatio *float64 `json:"completenessRatio,omitempty"`

	// Taille / durée
	TotalFileSizeBytes     int64 `json:"totalFileSizeBytes,omitempty"`
	AverageDurationSeconds int   `json:"averageDurationSeconds,omitempty"`
}

// IngestResponse — réponse de POST /api/v1/ingest
type IngestResponse struct {
	Success bool `json:"success"`
	Score   struct {
		Global        float64 `json:"global"`
		Certification string  `json:"certification"`
		Quantity      float64 `json:"quantity"`
		Quality       float64 `json:"quality"`
		Availability  float64 `json:"availability"`
		Freshness     float64 `json:"freshness"`
	} `json:"score"`
	Meta struct {
		TotalItems  int    `json:"totalItems"`
		MediaType   string `json:"mediaType"`
		ServiceType string `json:"serviceType"`
	} `json:"meta"`
}