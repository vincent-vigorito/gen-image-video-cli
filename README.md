# gen-image-video-cli

**CLI agent-native per generare immagini e video** con i principali provider AI —
Google Gemini, OpenAI, x.ai e OpenRouter (che aggiunge i motori ByteDance
Seedream/Seedance). Un solo binario, `giv`, progettato per essere **usato dagli
agenti** — Claude Code, LLM con tool-use, script e pipeline CI — prima ancora che
dagli umani: nessun prompt interattivo, output interamente parsabile, errori chiari.
E resta deliberatamente una CLI: **nessun server, nessun MCP, nessun runtime** — un
binario, le tue chiavi, e qualsiasi agente o script sa usarlo.

```bash
./bin/giv image --provider openrouter --model bytedance-seed/seedream-5-0-lite \
  --aspect 16:9 --input ref.webp --name hero "flat illustration of …"
```

```json
{
  "provider": "openrouter",
  "model": "bytedance-seed/seedream-5-0-lite",
  "prompt": "flat illustration of …",
  "files": [{ "path": "out/hero-20260814-160737.jpg", "mime": "image/jpeg", "bytes": 289327 }],
  "cost_usd": 0.011
}
```

## Perché "agent-native"

Un agente deve poter invocare il CLI e fidarsi del risultato senza euristiche:

- **stdout è solo dati** — un manifest JSON per invocazione. Log, progressi e avvisi
  vanno su stderr: `giv image … 2>/dev/null | jq .files[0].path` funziona sempre.
- **exit code parlanti** — 0 successo, 1 errore runtime (col messaggio API in chiaro
  su stderr), 2 uso errato.
- **manifest completo** — path dei file scritti, **mime reale** restituito dal provider
  (non dedotto dall'estensione), byte, e `cost_usd` quando il provider riporta il costo
  (OpenRouter, immagini e video): un agente con un budget sa quanto ha speso.
- **naming deterministico** — `<out>/<slug>-<timestamp>[-n].<ext>`; `--name` per slug
  stabili, `--out` per la directory.
- **credenziali senza interazione** — env di processo → `./credentials.env` → `./.env`;
  niente login, niente browser.
- **retry automatico sui 5xx transitori** — fino a 2 ritentativi con backoff (avviso su
  stderr): un blip del provider non fa fallire il task dell'agente.
- **riproducibilità** — `--seed` dove il provider lo supporta (Gemini, OpenRouter).
- **registro locale** — ogni generazione è appesa a `giv-log.jsonl` nella directory
  corrente; `giv log` la storia, `giv log --sum-cost` il totale speso registrato.
- **input da URL** — `--input` e `--image` accettano anche URL http(s), scaricati da soli.
- **prezzi interrogabili** — `giv models --provider openrouter --json` include il listino
  per modello: un agente può scegliere il motore economico senza tabelle hardcoded.
- **skill inclusa** — `skills/gen-image-video/SKILL.md`: guida operativa per agenti con
  matrice provider verificata, ricette e quirk noti.

## Quickstart

Richiede Go 1.26+.

```bash
make build                # → bin/giv
cp .env.example .env      # inserisci le chiavi dei provider che usi
./bin/giv models          # elenca i modelli (e verifica l'auth)
./bin/giv image "a red panda astronaut, flat illustration"
```

## Comandi

```bash
giv models [--provider P] [--all] [--json]   # modelli image/video disponibili
giv image  [flag] "<prompt>"                 # genera immagini
giv video  [flag] "<prompt>"                 # genera video (job asincrono, 1-5 min)
giv log    [-n N] [--sum-cost] [--json]      # registro locale delle generazioni
giv version
```

Flag comuni: `--provider` (gemini | openai | xai | openrouter), `--model` (default
sensato per provider), `--out`, `--name`. Il prompt è un **singolo argomento quotato**;
i flag possono stare prima o dopo.

| Comando | Flag specifici |
|---|---|
| `image` | `-n <num>` · `--aspect <1:1\|16:9\|9:16\|4:3\|3:4>` · `--seed <n>` · `--input <file\|url>` (ripetibile: reference di stile / editing) |
| `video` | `--aspect` · `--resolution <720p\|1080p>` · `--duration <s>` · `--negative "<t>"` · `--image <file\|url>` (frame iniziale, image-to-video) |

## Provider

| `--provider` | Immagini | Video | `--input` | `--aspect` | `--seed` |
|---|---|---|---|---|---|
| `gemini` (default) | ✅ Nano Banana, Imagen | ✅ Veo (`--duration` 4/6/8) | ✅ | ✅ | ✅ |
| `openai` | ✅ gpt-image-* | ✅ Sora (`sora-2`, 720p, `--duration` 4/8/12) | ✅ via edits | ✅ via size | ❌ |
| `xai` | ✅ grok-imagine-image-* | ✅ grok-imagine-video (fino a 15s) | ❌ immagini / ✅ video | ❌ immagini / ✅ video | ❌ |
| `openrouter` | ✅ Google, OpenAI, ByteDance | ✅ Seedance, Veo, Sora | ✅ | ✅ | ✅ |

Note:
- OpenRouter usa gli endpoint media dedicati (`/images`, `/videos` asincrono) — l'unica
  via per i modelli ByteDance; riporta `cost_usd` nel manifest.
- Sora con `--image` (input_reference): l'immagine deve avere **esattamente** la size
  del video richiesto.
- I nomi modello cambiano spesso: in caso di 404 rifare `giv models --provider <p>`.

## Uso da un agente (Claude Code)

La skill operativa è nel repo e si installa con un symlink:

```bash
ln -s "$(pwd)/skills/gen-image-video" ~/.claude/skills/gen-image-video
```

Da quel momento qualsiasi sessione sa usare il CLI: contratto di output, matrice
provider, ricette (reference di stile, image-to-video), costi indicativi e quirk.

## Multi-progetto

Per lavorare per-cliente senza toccare la config globale:

```bash
mkdir -p projects/<nome>          # cartella gitignorata: credenziali, reference, output
cd projects/<nome>
../../bin/giv image …             # legge ./credentials.env, scrive in ./out
```

## Struttura

```
cmd/giv/                  entrypoint e parsing comandi
internal/config/          risoluzione credenziali (env → credentials.env → .env)
internal/httpx/           errori HTTP tipizzati + retry sui 5xx
internal/provider/        interfaccia Provider + tipi comuni
internal/provider/gemini/ adapter Gemini (generateContent, predict, predictLongRunning)
internal/provider/openaicompat/  adapter OpenAI-compatible (openai, xai, openrouter, Sora)
internal/output/          salvataggio media + manifest JSON
skills/gen-image-video/   skill agent-native
```

## Versioning

SemVer, storia in [CHANGELOG.md](CHANGELOG.md). La versione del binario è in
`cmd/giv/main.go` (`const version`) e va aggiornata insieme al changelog a ogni release.
