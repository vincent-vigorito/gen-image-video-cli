---
name: gen-image-video
description: Generazione di immagini e video via CLI `giv` (gen-image-video-cli) sui provider Gemini, OpenAI, x.ai e OpenRouter (inclusi ByteDance Seedream/Seedance). Usare quando serve generare o modificare immagini da prompt, lavorare con reference di stile (--input), generare video text-to-video o animare un'illustrazione (image-to-video), o confrontare modelli/provider sullo stesso prompt.
---

# giv — CLI generazione immagini e video

## Setup

- Repo: clone di `gen-image-video-cli` — binario `bin/giv`
  (se manca: `make build` dalla radice del repo, richiede Go 1.26+).
- Chiavi e `GIV_PROVIDER` si risolvono in ordine: env di processo → `./credentials.env` →
  `./.env` → `~/.config/giv/credentials.env` (vince il primo che le definisce). L'ultimo
  file rende `giv` usabile da qualsiasi directory, senza spostarsi nel repo.
- Chiavi: `GEMINI_API_KEY`, `OPENAI_API_KEY`, `XAI_API_KEY`, `OPENROUTER_API_KEY`
  (bastano quelle dei provider che si usano). `GIV_PROVIDER` cambia il provider usato
  senza `--provider` (default `gemini`).
- Multi-progetto: `projects/<nome>/` con un proprio `credentials.env`, poi
  `cd projects/<nome> && ../../bin/giv …` (la cartella è gitignorata: può tenere
  anche reference e output del cliente).

## Contratto output (per parsing)

- **stdout: solo il manifest JSON** `{provider, model, prompt, files:[{path, mime, bytes}], cost_usd?}`.
  Log, avvisi e puntini di polling vanno su stderr. `cost_usd` c'è quando il provider
  riporta il costo (OpenRouter): usarlo per tracciare il budget.
- Exit code (il messaggio d'errore API e un `suggerimento:` sono in chiaro su stderr):
  0 ok · 1 errore generico · 2 uso errato (flag, prompt, provider o modello; anche video
  openai e video gemini, anche con `--model`) · 3 modello non trovato (404: `giv models --provider P`) ·
  4 chiave rifiutata o credito esaurito (401/402/403: cambiare provider o ricaricare) ·
  5 errore dell'API o di rete · 7 troppe richieste (429: aspettare) · 10 chiave API mancante.
  Decidere dal codice: 3 → altro modello, 4 → altro provider, 7 → attendere e riprovare.
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
giv log    [-n N] [--sum-cost] [--json]         # registro generazioni (giv-log.jsonl nella cwd)
giv version
```

`--input` e `--image` accettano **anche URL http(s)**: vengono scaricati da soli (niente
curl preventivo). `giv log --sum-cost` dà il totale speso registrato nella directory —
usalo per riferire i costi a fine lavoro. Su openrouter `models --json` include il
`pricing` per modello: consultalo per scegliere il motore economico.

Flag comuni: `--provider` (gemini|openai|xai|openrouter, default `GIV_PROVIDER` se
impostato, altrimenti gemini), `--model` (default per provider: tabella sotto, anche in
`giv --help`), `--out`, `--name`. Il prompt va passato come **singolo argomento
quotato**; i flag possono stare prima o dopo.

`image`: `-n <num>` (su openrouter una richiesta per immagine, costi sommati in
`cost_usd`; se una fallisce le già generate non vengono salvate), `--aspect <1:1|16:9|9:16|4:3|3:4>`, `--seed <n>` (riproducibilità:
gemini e openrouter, altrove ignorato con avviso), `--input <file>` ripetibile
(reference di stile / editing).
`video`: `--aspect`, `--resolution <720p|1080p>`, `--duration <s>`, `--negative`,
`--image <file>` (frame iniziale, image-to-video).

I 5xx transitori vengono ritentati da soli (2 tentativi con backoff, avviso su stderr):
non serve gestire i retry lato agente. Nei video l'attesa del job riprova anche su 429 ed
errori di rete (fino a 5 volte, rispettando `Retry-After`). La richiesta che **avvia** il
video invece non viene mai ripetuta dopo un 5xx o un timeout: il job potrebbe essere già
partito e un secondo invio costerebbe due video. Se esce con codice 5, **non rilanciare
subito**: cercare il job sul provider (l'id è nel messaggio d'errore quando il job è
partito) e solo dopo decidere se riprovare.

## Matrice provider (default dal codice, modelli da `giv models` del 13/11/2026)

| provider | immagini | video | `--input` | `--aspect` | modelli chiave |
|---|---|---|---|---|---|
| `gemini` | ✅ | ❌ | ✅ | ✅ | immagini `gemini-3.1-flash-image` (default, Nano Banana 2), `gemini-3-pro-image` (top), `gemini-3.1-flash-lite-image`, `gemini-2.5-flash-image`; nessuna API video (Veo spento), usare openrouter |
| `openai` | ✅ | ❌ | ✅ (via edits) | ✅ (mappato su size) | `gpt-image-2.6-flare` (default), `gpt-image-2.6-sunburst`, `gpt-image-2.5-flare`/`-sunburst`, `gpt-image-2`, `gpt-image-1.5`, `gpt-image-1-mini`; nessuna API video |
| `xai` | ✅ | ✅ | ❌ img / ✅ video | ❌ img / ✅ video | `grok-imagine-image-2.0` (default), `grok-imagine-image`; video `grok-imagine-video-1.5` (default, fino a 15s, 480p default, image-to-video ok), `grok-imagine-video-1.5-lite` |
| `openrouter` | ✅ | ✅ | ✅ | ✅ | immagini `google/gemini-3.1-flash-image` (default), `google/gemini-3-pro-image`, `openai/gpt-image-2.6-flare`, `bytedance-seed/seedream-5-0-lite\|pro`, Flux 2, Recraft v4…; video `google/gemini-omni-1.2-flash` (default), `bytedance/seedance-2.0-mini`, `bytedance/seedance-2.5`, `google/veo-3.1` / `-fast` / `-lite`, Kling, Wan… |

- **Video gemini**: non supportato (Veo 3.1 spento sulla Gemini API dal 22/10/2026, percorso
  rimosso): `giv video --provider gemini` esce subito con codice 2, anche con `--model`,
  senza chiamate di rete, e indica `--provider openrouter --model google/gemini-omni-1.2-flash`.
  Veo resta su openrouter: `google/veo-3.1`, `-fast`, `-lite` (durate 4|6|8).
- **Video openai**: non supportato (Sora spento da OpenAI il 24/09/2026, rimosso nella 0.7.0): `giv video --provider openai`
  esce subito con codice 2, senza chiamate di rete. Usare `--provider openrouter` o `xai`.
- Per confronti multi-provider: stesso prompt, `--name` diverso per provider.

## Ricette

**Immagine con reference di stile** (metodo usato per lo stile Alegria — ricetta completa
in `dati-siti/STILE-ILLUSTRAZIONI-ALEGRIA.md` del workspace):

```bash
./bin/giv image --model gemini-3-pro-image --aspect 16:9 --name <slug> \
  --input <URL-reference-o-file> \
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

## Costi

- Il costo reale di una chiamata è `cost_usd` nel manifest (openrouter); il totale
  registrato è `giv log --sum-cost`: usarli per il budget.
- Il listino è `giv models --provider openrouter --json`, campo `pricing` (dollari per
  token o per unità, come lo riporta il provider). Per molti modelli video riporta 0 (es.
  `bytedance/seedance-2.0-mini`, `google/veo-3.1`, `x-ai/grok-imagine-video-1.5`): non
  vuol dire gratis, il costo vero è solo nel `cost_usd` dopo la chiamata. Per gemini,
  openai e xai diretti `models` non riporta prezzi.
- Prezzi video indicativi su openrouter (listino del 20/11/2026, $ al secondo a 720p, tra
  parentesi senza audio): `google/gemini-omni-1.2-flash` 0,07 (audio sempre incluso;
  1080p 0,10), `google/veo-3.1-lite` 0,05 (0,03), `google/veo-3.1-fast` 0,10 (0,08),
  `google/veo-3.1` 0,40 (0,20). Veo e Sora diretti non esistono più (Gemini e OpenAI).
- Per prove ripetute usare i modelli flash/lite/mini e `--duration 4`; il modello top
  solo per l'output finale. `-n N` su openrouter costa N richieste.

## Quirk noti

- **OpenRouter**: `giv models --provider openrouter` elenca l'intero catalogo immagini
  e video (dalla v0.6.0; prima mancavano ByteDance e tutti i video); scheda di un modello
  via `GET /api/v1/models/<id>/endpoints`.
- **HTTP 500 dal provider "Seed"** (ByteDance su OpenRouter): instabilità temporanea
  dell'endpoint, riprovare più tardi — non è un errore del CLI.
- I nomi modello **cambiano spesso** (es. `grok-2-image` → `grok-imagine-image-*`;
  ritirati `veo-3.0-*` e `imagen-4.0-*`; al 13/11/2026 non sono più elencati `gpt-image-1`
  su openai né modelli video su gemini): in caso di 404 sul modello (codice 3), rifare
  `giv models --provider <p>`.
- La clausola "NO text, NO letters" nel prompt va **sempre** tenuta per le illustrazioni:
  tutti i modelli tendono a scrivere etichette.
