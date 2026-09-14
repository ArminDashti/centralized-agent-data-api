# centralized-agent-data-api

Gin + PostgreSQL API for Cursor session extracts (context %, tokens, thinking, useful turn metadata).

## Run

```powershell
cd C:\Users\armin\GitHub\centralized-agent-data-api
Copy-Item .env.example .env
docker compose up -d
go mod tidy
go run ./cmd/server
```

- Health: `GET http://localhost:8210/health`
- Login: `POST /api/v1/auth/login` body `{"username":"armin","password":"dopadopa123"}`
- Ingest: `POST /api/v1/sessions/ingest` (Bearer) — raw extract JSON, `{ "extract": {...}, "tokens": {...} }`, or multipart `file`
- List: `GET /api/v1/sessions`
- Session: `GET /api/v1/sessions/:uuid`
- Thinking: `GET /api/v1/sessions/:uuid/thinking`
- Turns: `GET /api/v1/sessions/:uuid/turns`

Postgres host port: **5465**. API listen: **:8210**.
