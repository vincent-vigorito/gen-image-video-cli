---
name: gen-image-video
description: Generazione di immagini e video via CLI `giv` (gen-image-video-cli) sui provider Gemini, OpenAI, x.ai e OpenRouter (inclusi ByteDance Seedream/Seedance). Usare quando serve generare o modificare immagini da prompt, lavorare con reference di stile (--input), generare video text-to-video o animare un'illustrazione (image-to-video), o confrontare modelli/provider sullo stesso prompt.
---

# giv — CLI generazione immagini e video

## Setup

- Repo: `~/Documents/Prototipi/cli-swerpify/gen-image-video-cli` — binario `bin/giv`
  (se manca: `make build`, richiede Go 1.26+).
- **Eseguire dalla directory del repo** (o da una cartella progetto): le chiavi si
  risolvono in ordine env di processo → `./credentials.env` → `./.env`.
- Chiavi: `GEMINI_API_KEY`, `OPENAI_API_KEY`, `XAI_API_KEY`, `OPENROUTER_API_KEY`
  (bastano quelle dei provider che si usano).
- Multi-progetto: `projects/<nome>/` con un proprio `credentials.env`, poi
  `cd projects/<nome> && ../../bin/giv …` (la cartella è gitignorata: può tenere
  anche reference e output del cliente).

## Contratto output (per parsing)

- **stdout: solo il manifest JSON** `{provider, model, prompt, files:[{path, mime, bytes}]}`.
  Log, avvisi e puntini di polling vanno su stderr.
- Exit code: 0 ok, 1 errore runtime (il messaggio d'errore API è in chiaro su stderr), 2 uso errato.
- File salvati come `<out>/<slug>-<timestamp>[-n].<ext>` — `--name` per slug stabile,
  `--out` per la directory (default `out/`).
- Il mime nel manifest è quello **reale** restituito dal provider (Nano Banana → jpeg,
  gpt-image → png…): leggerlo dal manifest, non dedurlo.
- Una chiamata può restituire **più file** (es. OpenRouter a volte dà 2 varianti): iterare su `files[]`.

## Comandi

```bash
giv models [--provider P] [--all] [--json]      # elenca modelli image/video (verifica anche l'auth)
giv image  [flag] "<prompt>"                    # genera immagini
giv video  [flag] "<prompt>"                    # genera video (job asincrono, 1-5 min)
giv version
```

Flag comuni: `--provider` (gemini|openai|xai|openrouter, default gemini), `--model`
(default sensato per provider), `--out`, `--name`. Il prompt va passato come **singolo
argomento quotato**; i flag possono stare prima o dopo.

`image`: `-n <num>`, `--aspect <1:1|16:9|9:16|4:3|3:4>`, `--input <file>` ripetibile
(reference di stile / editing).
`video`: `--aspect`, `--resolution <720p|1080p>`, `--duration <s>`, `--negative`,
`--image <file>` (frame iniziale, image-to-video).

## Matrice provider (verificata 14/08/2026)

| provider | immagini | video | `--input` | `--aspect` | modelli chiave |
|---|---|---|---|---|---|
| `gemini` | ✅ | ✅ Veo | ✅ | ✅ | `gemini-3-pro-image` (top), `gemini-2.5-flash-image` (default), `imagen-4.0-*`; video `veo-3.1-fast-generate-preview` |
| `openai` | ✅ | ❌ (Sora in roadmap) | ✅ (via edits) | ✅ (mappato su size) | `gpt-image-1` (default), `gpt-image-2` (top) |
| `xai` | ✅ | ❌ | ❌ | ❌ | `grok-imagine-image-2.0` (default), `-quality` |
| `openrouter` | ✅ | ✅ | ✅ | ✅ | `google/gemini-3-pro-image`, `bytedance-seed/seedream-5-0-lite|pro`; video `bytedance/seedance-2.0-mini` (economico), `google/veo-3.1` (default) |

- Veo (gemini): `--duration` 4|6|8 — il default del modello è 8s e **costa il doppio** di 4.
- Per confronti multi-provider: stesso prompt, `--name` diverso per provider.

## Ricette

**Immagine con reference di stile** (metodo usato per lo stile Alegria — ricetta completa
in `dati-siti/STILE-ILLUSTRAZIONI-ALEGRIA.md` del workspace):

```bash
curl -sf -o /tmp/ref.webp <URL-reference>
./bin/giv image --model gemini-3-pro-image --aspect 16:9 --name <slug> \
  --input /tmp/ref.webp \
  "<stile richiesto, EXACT same style as the reference image> CHANGE the palette to: <hex+ruoli>. Subject: <scena>. NO text, NO words, NO letters, NO numbers anywhere."
```

**Animare un'illustrazione** (image-to-video, economico):

```bash
./bin/giv video --provider openrouter --model bytedance/seedance-2.0-mini \
  --duration 4 --resolution 720p --aspect 16:9 --image out/<illustrazione>.jpg \
  --name <slug> "Bring this illustration to life with subtle 2D motion... Keep the EXACT flat style, colors and composition. NO text, NO camera movement."
```

**Verifica visiva — SEMPRE, dopo ogni generazione**: guardare l'immagine (Read del file);
per i video estrarre 2-3 frame (`ffmpeg -sseof -0.2 -i <mp4> -frames:v 1 /tmp/last.png`)
e controllare: stile coerente, palette giusta, **nessun testo/lettera** indesiderato.

## Costi indicativi (agosto 2026)

- Immagini: Nano Banana flash ~$0.04 · Nano Banana Pro ~$0.13-0.25 · gpt-image-2 ~$0.25 ·
  Seedream Lite pochi cent · grok ~$0.07.
- Video: seedance-2.0-mini 4s/720p ~$0.12 · veo-3.1-fast 4s ~$0.60.
- Per prove ripetute usare i modelli flash/lite/mini e `--duration 4`; il modello top
  solo per l'output finale.

## Quirk noti

- **OpenRouter, modelli ByteDance**: non compaiono in `giv models` ma esistono — usare
  gli id noti; scheda modello via `GET /api/v1/models/<id>/endpoints`.
- **HTTP 500 dal provider "Seed"** (ByteDance su OpenRouter): instabilità temporanea
  dell'endpoint, riprovare più tardi — non è un errore del CLI.
- **Modelli Imagen**: non accettano `--input` (il CLI lo segnala); per editing usare
  i modelli `gemini-*-image`.
- I nomi modello **cambiano spesso** (es. `grok-2-image` sparito, sostituito da
  `grok-imagine-image-*`): in caso di 404 sul modello, rifare `giv models --provider <p>`.
- La clausola "NO text, NO letters" nel prompt va **sempre** tenuta per le illustrazioni:
  tutti i modelli tendono a scrivere etichette.
