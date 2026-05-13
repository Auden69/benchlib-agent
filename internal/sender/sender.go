package sender

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/benchlib/agent/internal/payload"
)

const (
	maxRetries  = 3
	httpTimeout = 30 * time.Second
)

type Result struct {
	Score         float64
	Certification string
	LibraryID     string
	DurationMs    int
	Attempts      int
	Error         string
	UserError     string // message clair pour l'utilisateur
	DebugError    string // détail technique pour le debug
}

func Send(apiURL, apiKey string, p *payload.IngestPayload) Result {
	body, err := json.Marshal(p)
	if err != nil {
		return Result{Error: fmt.Sprintf("json marshal: %v", err)}
	}

	client := &http.Client{Timeout: httpTimeout}
	backoff := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}

	for attempt := 1; attempt <= maxRetries; attempt++ {
		start := time.Now()
		req, err := http.NewRequest("POST", apiURL+"/api/v1/ingest", bytes.NewReader(body))
		if err != nil {
			return Result{Error: fmt.Sprintf("création requête: %v", err), Attempts: attempt}
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-BenchLib-Key", apiKey)

		resp, err := client.Do(req)
		durationMs := int(time.Since(start).Milliseconds())

		if err != nil {
			if attempt == maxRetries {
				return Result{
					Error:      fmt.Sprintf("envoi échoué après %d tentatives", maxRetries),
					UserError:  "Impossible de contacter le serveur BenchLib — vérifie l'URL API dans Paramètres et ta connexion internet",
					DebugError: fmt.Sprintf("après %d tentatives : %v", maxRetries, err),
					Attempts:   attempt,
				}
			}
			time.Sleep(backoff[attempt-1])
			continue
		}
		defer resp.Body.Close()

		// Pas de retry sur 401/403/429
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			return Result{
				Error:      fmt.Sprintf("auth échouée (%d)", resp.StatusCode),
				UserError:  "Clé API non reconnue ou révoquée — vérifie ta clé dans Paramètres",
				DebugError: fmt.Sprintf("HTTP %d sur POST /api/v1/ingest", resp.StatusCode),
				Attempts:   attempt,
			}
		}
		if resp.StatusCode == 429 {
			return Result{
				Error:      "rate limit atteint (429)",
				UserError:  "Trop de requêtes envoyées — réessaie dans quelques minutes",
				DebugError: "HTTP 429 — quota de l'API BenchLib dépassé",
				Attempts:   attempt,
			}
		}
		if resp.StatusCode == 400 {
			respBody, _ := io.ReadAll(resp.Body)
			body := string(respBody)
			return Result{
				Error:      fmt.Sprintf("payload invalide (400): %s", body),
				UserError:  "Le payload envoyé est invalide — une mise à jour de l'agent est peut-être nécessaire",
				DebugError: fmt.Sprintf("HTTP 400 — réponse serveur : %s", body),
				Attempts:   attempt,
			}
		}
		if resp.StatusCode == 403 {
			return Result{
				Error:      "scan désactivé (403)",
				UserError:  "Le scan est désactivé pour cette bibliothèque sur BenchLib",
				DebugError: "HTTP 403 — scanEnabled=false côté BenchLib",
				Attempts:   attempt,
			}
		}

		if resp.StatusCode >= 500 && attempt < maxRetries {
			time.Sleep(backoff[attempt-1])
			continue
		}

		if resp.StatusCode >= 500 {
			respBody, _ := io.ReadAll(resp.Body)
			return Result{
				Error:      fmt.Sprintf("erreur serveur (%d)", resp.StatusCode),
				UserError:  "Le serveur BenchLib a rencontré une erreur — réessaie dans quelques minutes",
				DebugError: fmt.Sprintf("HTTP %d — %s", resp.StatusCode, string(respBody)),
				Attempts:   attempt,
			}
		}

		if resp.StatusCode != 200 && resp.StatusCode != 201 {
			return Result{
				Error:      fmt.Sprintf("réponse inattendue (%d)", resp.StatusCode),
				UserError:  fmt.Sprintf("Réponse inattendue du serveur BenchLib (code %d)", resp.StatusCode),
				DebugError: fmt.Sprintf("HTTP %d sur POST /api/v1/ingest", resp.StatusCode),
				Attempts:   attempt,
			}
		}

		var result payload.IngestResponse
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return Result{
				Error:      "décodage réponse échoué",
				UserError:  "Réponse inattendue de BenchLib — une mise à jour de l'agent est peut-être nécessaire",
				DebugError: fmt.Sprintf("json.Decode: %v", err),
				Attempts:   attempt,
			}
		}

		return Result{
			Score:         result.Score.Global,
			Certification: result.Score.Certification,
			DurationMs:    durationMs,
			Attempts:      attempt,
		}
	}
	return Result{
		Error:      "échec après toutes les tentatives",
		UserError:  "Impossible d'envoyer les données à BenchLib après plusieurs tentatives",
		DebugError: fmt.Sprintf("3 tentatives épuisées (backoff 2s/4s/8s)"),
		Attempts:   maxRetries,
	}
}