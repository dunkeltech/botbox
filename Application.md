# Software Requirements & Architecture Specification (Application.md)

## 1. Übersicht & Vision

Dieses Dokument beschreibt die Architektur und Spezifikation einer Multi-Agenten-Webanwendung. Die Anwendung ermöglicht es Benutzern, interaktive „Bot-Computer-Agenten“ zu erstellen und zu verwalten. Jeder Bot kombiniert ein Large Language Model (LLM) mit einer eigenen, isolierten Sandbox-Umgebung („Computer“), die über eine grafische Oberfläche (Web-GUI) einsehbar ist und vollen Zugriff auf das System besitzt.

---

## 2. Tech-Stack

* **Backend:** Go (Golang) — *Hohe Performance, exzellentes Concurrency-Handling für WebSockets/GRPC & Container-Orchestrierung.*
* **Frontend:** Next.js (React, TypeScript, Tailwind CSS) — *Server-Side Rendering, App Router, responsive Komponenten.*
* **Datenbank:** PostgreSQL — *Relationale Speicherung von Benutzern, Bot-Konfigurationen, Sessions und Logs.*
* **Orchestrierung/Sandboxing:** Docker Engine API & Kubernetes Go-Client API.

---

## 3. Architektur-Übersicht

```
+-----------------------------------------------------------------------+
|                             Frontend (Next.js)                         |
|  - Dashboard / Bot Builder   - Chat UI   - noVNC GUI Canvas Viewer     |
|  - Animated Avatar Renderer (Bottts / SVG)                             |
+---------------------------------------------------+-------------------+
                                                    | REST / WebSocket
+---------------------------------------------------v-------------------+
|                              Backend (Go)                             |
|  +-------------------+  +-------------------+  +-------------------+  |
|  | Auth Service      |  | Bot Engine        |  | Driver Manager    |  |
|  | (Modular Adapters)|  | (LLM Router & CLI)|  | (Docker / K8s)    |  |
|  +-------------------+  +-------------------+  +-------------------+  |
|  | Secrets Engine    |  | Egress Proxy Config|                        |
|  +-------------------+  +-------------------+                         |
+-------------------+-------------------------------+-------------------+
                    |                               |
          +---------v----------+         +----------v----------+
          |     PostgreSQL     |         | Compute Driver Layer|
          +--------------------+         +----------+----------+
                                                    |
                                       +------------+------------+
                                       |                         |
                           +-----------v-----------+ +-----------v-----------+
                           | Docker Host (Local)   | | Kubernetes Cluster    |
                           |  - Bot Sandbox Pod    | |  - Bot Sandbox Pod    |
                           |    ├─ Computer (GUI)  | |    ├─ Computer (GUI)  |
                           |    └─ Egress Proxy    | |    └─ Egress Proxy    |
                           +-----------------------+ +-----------------------+
```

---

## 4. Detaillierte Modulspezifikationen

### 4.1 Authentifizierung & Benutzerverwaltung (Modular)

Das Auth-System ist über ein `AuthProvider`-Interface modular aufgebaut, um künftige Anbindungen ohne Re-Engineering zu ermöglichen.

* **Phase 1:** Lokale Datenbank-Authentifizierung (Email + Gehashtes Passwort via Argon2id, JWT-Session-Tokens, Refresh-Token-Rotation).
* **Phase 2 (Zukunft):** SSO (OpenID Connect / OAuth2), LDAP / Active Directory, SAML 2.0.
* **Architektur-Muster:**
  ```go
  type AuthProvider interface {
      Authenticate(ctx context.Context, credentials Credentials) (*User, error)
      ValidateToken(ctx context.Context, token string) (*Claims, error)
  }
  ```

---

### 4.2 Bot-Engine & LLM-Anbindung Layer

Bots nutzen LLMs für Planungs- und Ausführungsentscheidungen. Die Anbindung erfolgt abstrahiert über zwei Hauptwege:

1. **Direct API Integrations (Provider):**
   * Anbindung an Gateway-Dienste wie OpenRouter, OpenAI, Anthropic via API-Key (vom User hinterlegt oder global).
2. **Local / Container CLI Tools (Execution Drivers):**
   * Ausführung direkt im Bot-Container über CLI-Tools:
     * `codex` / `codex-cli`
     * `antigravity` / `antigravity-cli`
     * `claude-cli`
   * Das Backend interagiert mit diesen Tools wahlweise per SSH, Subprocess Exec oder über ein internes Agent-Sidecar über gRPC/WebSockets.

---

### 4.3 Bot Sandbox ("Computer"-Umgebung)

Jeder Bot erhält ein eigenes isoliertes Betriebssystem (Debian/Ubuntu-basiertes Container-Image).

* **Features der Bot-Computer:**
  * **Grafische Oberfläche (GUI):** XFCE/LXDE/Openbox mit X11/VNC Server (z. B. `x11vnc` + `noVNC`). Das Frontend bettet den VNC-Canvas per WebSocket direkt im Browser ein.
  * **Webbrowser:** Vorinstallierter Chromium/Firefox für automatisierte oder manuelle Interaktionen.
  * **CLI & LLM Tools:** `codex-cli`, `antigravity`, `claude-cli` vorinstalliert und konfiguriert.
  * **Systemrechte:** Vollen `sudo` / `root`-Zugriff im Container, um eigenständig `apt-get`, `pip`, `npm` etc. nachzuinstallieren.
  * **Persistenz:** Optionales Anbinden von Persistent Volumes für das Home-Verzeichnis des Bots.

---

### 4.4 Compute Driver Abstraction Layer

Um die Bot-Umgebungen flexibel zu hosten, existiert ein abstrakter `ComputeDriver`:

```go
type ComputeDriver interface {
    CreateEnvironment(ctx context.Context, config BotConfig) (EnvID, error)
    DestroyEnvironment(ctx context.Context, id EnvID) error
    GetVNCStreamURL(ctx context.Context, id EnvID) (string, error)
    ExecCommand(ctx context.Context, id EnvID, cmd []string) (ExecResult, error)
}
```

* **Docker Local Driver:** Nutzt den lokalen Docker Socket (`/var/run/docker.sock`), um Container direkt auf dem Host zu starten. Ideal für Entwicklung und Single-Node Deployments.
* **Kubernetes Driver:** Startet Pods oder Custom Resources (CRDs) in einem K8s-Cluster. Unterteilt Umgebungen sauber per Namespaces und unterstützt Resource Quotas.

---

### 4.5 Secrets-Management & Egress Security Proxy

**Sicherheits-Grundsatz:** Sensible Credentials (z. B. GitHub/GitLab Personal Access Tokens, API-Keys) werden **niemals** direkt in den LLM-Prompt oder die System-Umgebungsvariablen des Bots geschrieben.

#### Funktionsweise des Egress Proxy (Sidecar-Muster):
1. Jedes Bot-Environment erhält einen transparenten HTTP/HTTPS Egress Proxy Sidecar (z. B. basierend auf Envoy oder einem maßgeschneiderten Go-Proxy).
2. Der Datenverkehr des Bots (z. B. `git clone https://gitlab.com/repo.git` oder REST-Calls) wird zwingend durch den Egress Proxy geleitet (`HTTP_PROXY` / `HTTPS_PROXY` Variablen & iptables redirection).
3. Wenn der Bot eine Anfrage an eine konfigurierte Domain (z. B. `gitlab.com` oder `github.com`) sendet, prüft der Proxy Platzhalter-Header oder Ziel-URLs und fügt das benötigte Auth-Token serverseitig ein.
4. **Ergebnis:** Der Bot kann ohne Kenntnis des echten Secrets auf externe Repositories/APIs zugreifen. Exfiltrationsversuche über das LLM schlagen fehl.

---

### 4.6 Robot-Style Animierte Avatare

Jeder Bot erhält bei der Erstellung automatisch einen einzigartigen, animierten Avatar.

* **Design-Richtlinie:**
  * Cybernetisch, metallisch, retro-futuristisch oder digital-robotisch (keine menschlichen Gesichter).
* **Technisches Konzept:**
  * **Generierung:** Nutzung einer SVG-basierten Seed-Engine (z. B. DiceBear *Bottts* / *Gridy* / Custom SVG Parts).
  * **Animation:** CSS3 keyframes / Lottie / Canvas-Animationen für subtile Effekte:
    * Pulsierende Augen / Sensoren
    * Rotierende Antennen / Zahnräder
    * Digitales Blinken (Idle State vs. Thinking State vs. Action State).

---

## 5. Datenmodell (PostgreSQL - Schema Entwurf)

```sql
-- Users Table
CREATE TABLE users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email VARCHAR(255) UNIQUE NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- Bots Table
CREATE TABLE bots (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID REFERENCES users(id) ON DELETE CASCADE,
    name VARCHAR(100) NOT NULL,
    description TEXT,
    avatar_seed VARCHAR(100) NOT NULL,
    llm_provider VARCHAR(50) NOT NULL, -- z.B. 'openrouter', 'claude-cli', 'codex-cli'
    llm_model VARCHAR(100) NOT NULL,
    driver_type VARCHAR(20) NOT NULL DEFAULT 'docker', -- 'docker' oder 'kubernetes'
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- Bot Secrets Table (Encrypted)
CREATE TABLE bot_secrets (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    bot_id UUID REFERENCES bots(id) ON DELETE CASCADE,
    domain_match VARCHAR(255) NOT NULL, -- z.B. 'github.com'
    header_name VARCHAR(100) NOT NULL, -- z.B. 'Authorization'
    secret_value_encrypted BYTEA NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- Chat Sessions & Messages
CREATE TABLE chat_sessions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    bot_id UUID REFERENCES bots(id) ON DELETE CASCADE,
    user_id UUID REFERENCES users(id) ON DELETE CASCADE,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE messages (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id UUID REFERENCES chat_sessions(id) ON DELETE CASCADE,
    sender VARCHAR(20) NOT NULL, -- 'user' oder 'bot'
    content TEXT NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);
```

---

## 6. Meilensteine & Entwicklungsplan

1. **Phase 1: Foundation & Auth**
   * Go Backend Setup mit Router & GORM/pgx.
   * PostgreSQL Integration.
   * Modularer Auth-Service (Lokal Auth).
   * Next.js Frontend Grundgerüst.

2. **Phase 2: Compute Driver & Bot Containers**
   * `ComputeDriver` Interface in Go implementieren.
   * Docker-Driver Prototyp (Container-Erstellung mit noVNC + XFCE).
   * WebSockets-Integration für noVNC Stream & Chat.

3. **Phase 3: LLM Integration & Secret Sidecar Proxy**
   * Einbindung von OpenRouter API & CLI Tool Wrapper (`codex`, `antigravity`, `claude`).
   * Aufbau des Egress-Proxy Sidecars zur Token-Injektion.
   * Absicherung der Sandbox (Netzwerkisolierung).

4. **Phase 4: Kubernetes Driver & Polish**
   * Kubernetes Driver Implementierung.
   * Generierung & Animation der Bot-Avatare im Next.js Frontend.
   * End-to-End Tests & Dokumentation.