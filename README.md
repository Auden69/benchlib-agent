# BenchLib Agent

**Agent local pour [BenchLib](https://benchlib.com)** — collecte les statistiques de ta bibliothèque multimédia et les envoie à BenchLib sans jamais exposer tes contenus.

Fonctionne avec **Plex**, **Navidrome**, et bientôt Audiobookshelf et Komga.

---

## Pourquoi un agent local ?

Jusqu'ici, BenchLib scannait les bibliothèques depuis ses propres serveurs — ce qui nécessitait d'exposer ton API publiquement. L'agent inverse ce modèle :

- L'agent tourne **sur ta machine**, interroge ton serveur en localhost
- Il n'envoie que des **statistiques agrégées et anonymisées**
- Aucun titre, nom de fichier, identifiant ou chemin ne quitte jamais ta machine

---

## Installation

### Docker (recommandé)

```yaml
# docker-compose.yml
services:
  benchlib-agent:
    image: ghcr.io/auden69/benchlib-agent:latest
    container_name: benchlib-agent
    restart: unless-stopped
    ports:
      - "8090:8090"
    volumes:
      - ./data:/data
    environment:
      - TZ=Europe/Paris
    network_mode: host
```

```bash
docker-compose up -d
```

### Windows — binaire avec icône systray

Télécharge `benchlib-agent-tray-windows-amd64.exe` depuis les [releases](https://github.com/Auden69/benchlib-agent/releases) et double-clique dessus.

L'agent s'installe automatiquement au démarrage Windows (registre `HKCU\...\Run`) et apparaît dans la barre des tâches.

### Linux / macOS — binaire

Télécharge le binaire depuis les [releases](https://github.com/Auden69/benchlib-agent/releases) :

```bash
# Linux
chmod +x benchlib-agent-linux-amd64
./benchlib-agent-linux-amd64

# macOS Intel
chmod +x benchlib-agent-macos-amd64
./benchlib-agent-macos-amd64

# macOS Apple Silicon (M1/M2/M3)
chmod +x benchlib-agent-macos-arm64
./benchlib-agent-macos-arm64
```

Interface disponible sur **http://localhost:8090**

---

## Interface web

L'agent embarque une interface complète accessible sur `http://localhost:8090` :

- **Dashboard** — métriques, bibliothèques actives, prochain scan
- **Connecteurs** — ajout/édition Plex, Navidrome ; test de connexion ; sélection des bibliothèques
- **Historique** — envois passés avec scores, certifications et payload JSON exact consultable
- **Paramètres** — clé API BenchLib, port, heure planifiée
- **Console** — logs en temps réel (vue utilisateur + vue debug)

---

## Connecteurs disponibles

| Connecteur     | Auth                | Bibliothèques          | Statut      |
| -------------- | ------------------- | ---------------------- | ----------- |
| Plex           | Token Plex          | Films, Séries, Musique | ✅ Dispo    |
| Navidrome      | Username / Password | Musique (Subsonic)     | ✅ Dispo    |
| Audiobookshelf | Token               | Audiobooks             | 🔜 Planifié |
| Komga          | Username / Password | BD, Mangas, Livres     | 🔜 Planifié |

---

## Configuration

Générée automatiquement au premier lancement dans `%APPDATA%\BenchLib\config.yaml` (Windows) ou `./data/config.yaml` (Docker).

```yaml
benchlib_api_key: "bl_xxxxxxxxxxxxxxxxxxxx"
port: 8090
schedule:
  hour: 3
  minute: 0
connectors:
  - type: plex
    name: Mon Plex
    url: http://192.168.1.100:32400
    public_url: https://plex.mondomaine.com # optionnel — active le monitoring uptime
    token: xxxxxxxxxxxxxxxxxxxx
    libraries:
      - id: "1"
        name: Films
        media_type: MOVIES
        enabled: true
```

L'URL de l'API BenchLib est embarquée dans le binaire. Elle peut être surchargée via `--api-url` ou `BENCHLIB_API_URL` (développement uniquement).

---

## Compiler depuis les sources

**Prérequis :** Go 1.25+

```bash
go mod tidy

# Linux / macOS
go build -ldflags="-s -w" -o benchlib-agent ./cmd/agent

# Windows systray (sans fenêtre console)
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w -H windowsgui" -o benchlib-agent-tray.exe ./cmd/tray
```

---

## Architecture

```
benchlib-agent/
├── cmd/
│   ├── agent/        ← point d'entrée daemon (Docker / Linux / macOS)
│   └── tray/         ← lanceur systray Windows (icône barre des tâches)
├── internal/
│   ├── config/       ← struct Config, load/save config.yaml
│   ├── connector/
│   │   ├── plex/     ← connecteur Plex
│   │   └── navidrome/← connecteur Navidrome (API Subsonic)
│   ├── payload/      ← types IngestPayload / IngestResponse
│   ├── scheduler/    ← cron, fusion par mediaType, historique
│   ├── sender/       ← POST /api/v1/ingest avec retry
│   └── web/          ← serveur HTTP + interface web embarquée
├── tools/
│   └── png2icon/     ← convertit icon.png → icon.go (ICO Windows)
├── Dockerfile
└── docker-compose.yml
```

**Fusion par mediaType** : si plusieurs bibliothèques Plex sont du même type (ex: Films + Animations + 4k → MOVIES), leurs stats sont additionnées avant envoi. Un seul payload par `connecteur × mediaType`.

**Jitter anti-surcharge** : l'heure de scan est décalée d'un offset 0–59 min calculé par hash de la clé API — déterministe et affiché dans les logs au démarrage.

---

## Confidentialité

Ce qui n'est **jamais** transmis à BenchLib :

- Titres de films, séries, livres ou pistes musicales
- Noms de fichiers ou chemins disque
- Identifiants TMDB/TVDB ou tout autre identifiant de contenu
- Adresse IP locale du serveur
- Identifiants de connexion

Seules des statistiques agrégées sont envoyées. Le payload exact est consultable à tout moment depuis l'interface agent (onglet Historique).

---

## Projets liés

- [jellyfin-plugin-benchlib](https://github.com/Auden69/jellyfin-plugin-benchlib) — Plugin natif Jellyfin (C# / .NET 9)

---

## Licence

MIT
