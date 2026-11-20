# Changelog

Tutte le modifiche rilevanti del progetto, nel formato
[Keep a Changelog](https://keepachangelog.com/it-IT/1.1.0/);
versioning [SemVer](https://semver.org/lang/it/).

## [Unreleased]

### Aggiunto

- `giv video`: **`--last-frame`** (ultimo fotogramma), **`--ref`** ripetibile (immagini di
  riferimento), **`--audio on|off`**, **`--seed`**, **`--param k=v`** ripetibile (parametro
  passante del modello), **`--timeout`** (tetto di attesa del job, default `15m`). Su
  **xai** diventano `last_frame`, `reference_images` e `generate_audio`; su
  **openrouter** `frame_images` con `last_frame`, `input_references`, `generate_audio`,
  `seed` e i parametri passanti. `--last-frame` e `--ref` accettano anche URL http(s).
- **Capacità e prezzi prima di spendere**: prima di avviare un video `giv` legge
  `/videos/models` di OpenRouter (gratuito) o una tabella interna per xai (20/11/2026). Un
  controllo strutturale non supportato (fotogrammi, riferimenti, durata, risoluzione,
  aspect, audio, `--param` fuori lista) esce con codice 2 prima di qualsiasi chiamata e
  indica i modelli dello stesso provider che lo supportano; uno di rifinitura (`--seed`,
  `--negative`, modello fuori catalogo) dà un avviso.
- `giv video --dry-run`: nessuna generazione, stampa in JSON parametri, prompt finale,
  avvisi e **costo stimato** (`cost_est_usd`, `cost_source`). `--max-cost <usd>` rifiuta
  con codice 2 una stima oltre il tetto o non stimabile; `--strict` rende errori anche i
  controlli di rifinitura.
- `giv models --kind image|video|other` filtra per tipo; con `--kind video --caps`
  (anche `--json`) mostra capacità e prezzo al secondo dei modelli video (xai, openrouter).
- **Preset di camera ed effetti**: `giv presets [--json]` elenca 15 preset di camera e 3
  di effetto, `giv presets show <nome> [<nome>]` mostra la frase finale per un modello
  (`--model`, `--prompt`, `--negative`, `--tutela`, `--strict`), senza chiamate.
  `giv video --preset <nome>` (al massimo uno di camera e uno di effetto) aggiunge al
  prompt la variante della famiglia del modello (kling, veo, seedance, wan; altrimenti la
  frase generica, con avviso); `--tutela prodotto|illustrazione|no` aggiunge una clausola
  (`prodotto` è il default con `--image`). I file `./giv-presets.json` e
  `~/.config/giv/presets.json` aggiungono o sostituiscono preset.
- **Manifest esteso**, solo con campi opzionali (i manifest di prima si leggono come
  prima): `command`, `prompt_final`, `presets[]`, `inputs` (file per ruolo con impronta
  sha256 breve), `params`, `job_id`, `duration_s` (durata reale del video),
  `cost_est_usd` e `cost_source` (stima da listino dove il provider non riporta il
  costo), `warnings[]`.
- **Registro**: una riga anche per i job video partiti e poi falliti o scaduti
  (`status`, `job_id`), che possono essere stati addebitati. `giv log --sum-cost`
  riporta separati il costo riportato (`cost_usd`) e quello stimato (`cost_est_usd`),
  più `failed_jobs` e `failed_cost_est_usd`; `--by provider|model` aggiunge i totali per
  gruppo.

### Modificato

- `--negative` su **openrouter**: prima ignorato con avviso, ora inviato dove il modello
  lo prevede (`negativePrompt`, `negative_prompt`); altrove resta un avviso.
- Su **xai** con `--image` un avviso dice che `--aspect` è ignorato (il video segue
  l'immagine).
- I controlli che il modello non supporta prima venivano inviati e fallivano dal provider
  (o venivano ignorati): ora escono con codice 2 prima della chiamata. I comandi di oggi
  mandano lo stesso corpo di richiesta e producono lo stesso manifest.

### Rimosso

- Video **gemini**: tolto il percorso Veo (`predictLongRunning`), spento sulla Gemini API
  dal 22/10/2026. `giv video --provider gemini` esce subito con codice 2, anche con
  `--model`, senza chiamate, e indica `--provider openrouter --model
  google/gemini-omni-1.2-flash` (Veo resta su OpenRouter: `google/veo-3.1`).

## [0.7.0] - 2026-11-13

### Aggiunto

- **Codici di uscita parlanti** (come gli altri CLI del progetto): 2 uso errato,
  3 modello non trovato (404), 4 autenticazione o credito (401/402/403), 5 errore
  dell'API o di rete, 7 troppe richieste (429), 10 chiave API mancante; 1 resta
  l'errore generico. Su stderr un `suggerimento:` indica cosa fare (es. `giv models
  --provider <p>` su un 404, cambiare provider su un 402).
- `GIV_PROVIDER`: provider usato senza `--provider` (env o file di configurazione);
  senza, resta `gemini`.
- Le credenziali (e `GIV_PROVIDER`) si leggono anche da
  `~/.config/giv/credentials.env`, ultimo fallback dopo env di processo,
  `./credentials.env` e `./.env`: `giv` funziona da qualsiasi directory.
- `image -n` maggiore di 1 su **OpenRouter**: una richiesta per immagine (l'endpoint
  `/images` accetta `n=1`), costi sommati in `cost_usd`. Prima rispondeva
  «n must be exactly 1».
- Il testo di aiuto (`giv --help`) genera la tabella dei modelli di default dalle stesse
  mappe usate dal codice: non può più divergere.

### Modificato

- Default immagine **openai**: `gpt-image-2.6-flare` (`gpt-image-1` non è più disponibile).
- Default video **openrouter**: `google/gemini-omni-1.2-flash` (al posto di
  `google/veo-3.1`).
- Video **gemini**: nessun default (`veo-3.1-fast-generate-preview` non è più
  disponibile). Senza `--model` esce con codice 2 e indica
  `--provider openrouter --model google/gemini-omni-1.2-flash`.
- L'**attesa dei video** (polling del job) ritenta 5xx, 429 ed errori di rete, fino a
  5 volte con backoff o `Retry-After`: un disturbo passeggero non butta più un video
  già avviato. L'id del job è sempre nel messaggio d'errore, per ritrovarlo sul provider.
- La richiesta che **avvia** un job video non viene più ripetuta dopo un 5xx o un
  timeout: il job potrebbe essere partito e un secondo invio farebbe pagare due video.
  Esce con codice 5 e un suggerimento.

### Rimosso

- Video **OpenAI Sora**: nessuna API video OpenAI utilizzabile. `giv video --provider
  openai` esce subito con codice 2 e indica `openrouter` o `xai`.

## [0.6.0] - 2026-09-22

### Modificato

- Default video **gemini**: `veo-3.1-fast-generate-preview` (il precedente
  `veo-3.0-fast-generate-001` è stato ritirato e rispondeva 404).
- Default immagine **gemini** e **openrouter**: Nano Banana 2
  (`gemini-3.1-flash-image` / `google/gemini-3.1-flash-image`) al posto di
  `gemini-2.5-flash-image`.
- `models --provider openrouter` interroga `/models?output_modalities=all`:
  senza filtro l'API elencava solo i modelli con output testuale (11 modelli
  immagine, nessun video). Ora compaiono l'intero catalogo immagini e video
  (ByteDance, Flux, Recraft, Kling, Wan…) col relativo pricing.

### Rimosso

- Supporto **Imagen** (`:predict`): i modelli `imagen-4.0-*` sono stati ritirati
  dalla Gemini API.

## [0.5.0] - 2026-08-14

### Aggiunto

- `--input` e `--image` accettano **URL http(s)**: il file viene scaricato in
  automatico in tmp (estensione dall'URL o dal Content-Type). La ricetta con
  reference dai tenant non richiede più il curl preventivo.
- **Registro locale delle generazioni**: ogni manifest viene appeso a
  `giv-log.jsonl` nella directory corrente (gitignorato). Nuovo comando
  `giv log` (`-n`, `--json`) e `giv log --sum-cost` per il totale speso.
- `models --json` su openrouter include il **pricing per modello** (raw del
  provider): permette di scegliere il motore economico senza tabelle hardcoded.
- Video **x.ai grok-imagine** (`/videos/generations` asincrona con polling):
  default `grok-imagine-video-1.5`, `--duration` fino a 15s, `--aspect`,
  `--resolution`, `--image` come data URL. Testato image-to-video (480p default).
  La matrice video è ora completa su tutti e quattro i provider.

## [0.4.0] - 2026-08-14

### Aggiunto

- `cost_usd` nel manifest quando il provider riporta il costo della chiamata
  (OpenRouter, immagini e video).
- **Retry automatico sui 5xx transitori** su tutti i provider: fino a 2
  ritentativi con backoff 2s/4s, avviso su stderr (`internal/httpx`).
- Video **OpenAI Sora** (`/videos` asincrono con polling): default `sora-2`,
  `--duration` 4|8|12, 720p; `--image` inviato come `input_reference` multipart
  (deve avere esattamente la size del video). Testato end-to-end (4s, 1280×720).
- `image --seed`: riproducibilità su gemini (`generationConfig.seed` /
  `parameters.seed`) e openrouter (`seed`); su openai/xai viene ignorato con
  avviso.
- Skill agent-native `skills/gen-image-video/SKILL.md`: contratto di output,
  matrice provider verificata, ricette (reference di stile, image-to-video),
  costi indicativi e quirk noti. Installabile con un symlink in `~/.claude/skills/`.

### Modificato

- README riscritto in chiave **agent-native**: contratto di output in apertura,
  matrice provider con capacità per flag, sezione "Uso da un agente".

## [0.3.0] - 2026-08-14

### Modificato

- OpenRouter migrato agli endpoint media dedicati (`/images` e `/videos`) al
  posto delle chat completions con `modalities`: sono l'unica via supportata
  dai modelli ByteDance (Seedream/Seedance) e abilitano `--aspect`
  (`aspect_ratio`) e `--input` (`input_references`) su tutti i motori.
  Verificato con `bytedance-seed/seedream-5-0-lite` (con reference) e con
  `google/gemini-2.5-flash-image`.

### Aggiunto

- Video via OpenRouter (`/videos`, job asincrono con polling): `--duration`,
  `--resolution`, `--aspect`, `--image` usato come `first_frame`. Testato con
  `bytedance/seedance-2.0-mini` (4s 720p, $0.12). Default: `google/veo-3.1`.
  Il download del risultato passa dalle content URL dell'host API con
  Authorization (`downloadAuth`, mai usato su CDN esterne).

## [0.2.0] - 2026-08-14

### Aggiunto

- Adapter **openaicompat**: provider `openai`, `xai` e `openrouter` per le immagini.
  OpenAI e x.ai via `images/generations` (per OpenAI anche `images/edits` multipart
  quando si passa `--input`); OpenRouter via chat completions con `modalities`
  (accetta `--input` come data URL). Video su questi provider: non ancora
  supportato (in roadmap: Sora).
- Flag `--provider` su `models`, `image` e `video` (default: `gemini`) con
  modello di default per provider.
- `image --aspect` mappato sul parametro `size` per OpenAI; avviso su stderr
  dove l'aspect non è supportato (x.ai, OpenRouter).
- `video --duration <s>`: durata del clip in secondi (Veo 3.1: 4, 6 o 8;
  0 = default del modello). Testato end-to-end con `veo-3.1-fast-generate-preview`
  in image-to-video.

### Modificato

- Default x.ai: `grok-imagine-image-2.0` (la famiglia `grok-2-image` non è più
  esposta dall'API).

## [0.1.0] - 2026-08-14

### Aggiunto

- Adapter **Gemini** (Google AI Studio): immagini via `generateContent`
  (Nano Banana, con `--input` per editing/reference) e via `predict` (Imagen),
  video **Veo** via `predictLongRunning` con polling e download del risultato.
- Comando `models`: elenco dei modelli image/video del provider (`--all`, `--json`).
- Comando `image`: flag `-n`, `--aspect`, `--input` (ripetibile), `--out`, `--name`.
- Comando `video`: flag `--aspect`, `--resolution`, `--negative`, `--image`
  (image-to-video), `--out`, `--name`.
- Comando `version`.
- Risoluzione credenziali a cascata: env di processo → `./credentials.env` → `./.env`.
- Output agent-native: media su file (`<slug>-<timestamp>[-n].<ext>`),
  manifest JSON su stdout, log su stderr.
