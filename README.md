# gen-image-video-cli

CLI agent-native per generare **immagini e video** dai provider AI (Google Gemini oggi;
OpenAI, x.ai e OpenRouter in roadmap). Pensata per essere pilotata da un LLM o da
script: log su stderr, **manifest JSON su stdout**, chiavi API in `.env`.

## Build

Richiede Go 1.26+.

```bash
make build        # produce bin/giv
```

## Configurazione

Le chiavi si risolvono in ordine: variabili d'ambiente → `./credentials.env` → `./.env`.

```bash
cp .env.example .env
# inserisci le chiavi dei provider che usi
```

Per lavorare per-progetto senza toccare la config globale: cartella
`projects/<nome>/` con il suo `credentials.env` (tutta `projects/` è ignorata da git).

## Uso

```bash
# elenca i modelli image/video disponibili sul tuo account
./bin/giv models [--all] [--json]

# genera immagini (provider di default: gemini)
./bin/giv image --model gemini-3-pro-image --aspect 16:9 -n 2 "a cat astronaut"

# stesso prompt su un altro provider
./bin/giv image --provider openai --model gpt-image-2 --aspect 16:9 "a cat astronaut"

# editing / reference di stile (solo modelli gemini-*-image, --input ripetibile)
./bin/giv image --input ref.webp "same style, new subject"

# genera un video (Veo, job asincrono ~1-5 min)
./bin/giv video --resolution 720p "a drone shot over the sea"

# versione
./bin/giv version
```

I file finiscono in `out/` (`--out` per cambiarla) con nome
`<slug>-<timestamp>[-n].<ext>` (`--name` per forzare lo slug). Su stdout esce il
manifest JSON, l'unico output pensato per il parsing:

```json
{
  "provider": "gemini",
  "model": "gemini-3-pro-image",
  "prompt": "…",
  "files": [{ "path": "out/….jpg", "mime": "image/jpeg", "bytes": 404722 }]
}
```

## Provider

| Provider (`--provider`) | Immagini | Video | Note |
|---|---|---|---|
| `gemini` (default) | ✅ Nano Banana, Imagen | ✅ Veo | API nativa `generativelanguage`; `--input` supportato |
| `openai` | ✅ gpt-image-* | 🔜 Sora | `--input` via `images/edits`, `--aspect` via `size` |
| `xai` | ✅ grok-imagine-image-* | 🔜 | niente `--input` né `--aspect` (limiti API) |
| `openrouter` | ✅ motori Google e OpenAI | — | via chat completions; `--input` ok, `--aspect` no |

## Struttura

```
cmd/giv/                  entrypoint e parsing comandi
internal/config/          risoluzione credenziali (env → credentials.env → .env)
internal/provider/        interfaccia Provider + tipi comuni
internal/provider/gemini/ adapter Gemini (generateContent, predict, predictLongRunning)
internal/output/          salvataggio media + manifest JSON
```

## Versioning

SemVer, storia in [CHANGELOG.md](CHANGELOG.md). La versione del binario è in
`cmd/giv/main.go` (`const version`) e va aggiornata insieme al changelog a ogni release.
