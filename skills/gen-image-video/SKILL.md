---
name: gen-image-video
description: Generazione di immagini e video via CLI `giv` (gen-image-video-cli) sui provider Gemini, OpenAI, x.ai e OpenRouter (inclusi ByteDance Seedream/Seedance). Usare quando serve generare o modificare immagini da prompt, lavorare con reference di stile (--input), generare video text-to-video o animare un'illustrazione (image-to-video, anche con ultimo fotogramma e immagini di riferimento), fare spot da foto di prodotto o mascotte coerenti fra scene con preset di camera ed effetti, controllare capacità e costo di un video prima di spendere (--dry-run, --max-cost), recuperare un job video già partito senza pagarlo due volte (giv job fetch), preparare uno spot a più scene con voce fuori campo da uno storyboard JSON (giv story check, giv story run, giv story cut), o confrontare modelli/provider sullo stesso prompt.
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
  riporta il costo (OpenRouter): usarlo per tracciare il budget. Campi opzionali, assenti
  quando non servono: `command`, `prompt_final` (inviato coi preset), `presets[]`,
  `inputs` (`image`/`last_frame`/`refs[]` con `sha256` breve), `params`, `job_id`,
  `duration_s`, `cost_est_usd` + `cost_source` (stima da listino dove il costo non è
  riportato, es. xai), `warnings[]` (controlli ignorati).
- Exit code (il messaggio d'errore API e un `suggerimento:` sono in chiaro su stderr):
  0 ok · 1 errore generico · 2 uso errato (flag, prompt, provider o modello; anche video
  openai e video gemini, anche con `--model`) · 3 modello non trovato (404: `giv models --provider P`) ·
  4 chiave rifiutata o credito esaurito (401/402/403: cambiare provider o ricaricare) ·
  5 errore dell'API o di rete · 7 troppe richieste (429: aspettare) · 10 chiave API mancante.
  Decidere dal codice: 3 → altro modello, 4 → altro provider, 7 → attendere e riprovare,
  5 su un video con l'id del job nel messaggio → `giv job fetch`, non un nuovo `giv video`.
- File salvati come `<out>/<slug>-<timestamp>[-n].<ext>` — `--name` per slug stabile,
  `--out` per la directory (default `out/`).
- Il mime nel manifest è quello **reale** restituito dal provider (Nano Banana → jpeg,
  gpt-image → png…): leggerlo dal manifest, non dedurlo.
- Una chiamata può restituire **più file** (es. OpenRouter a volte dà 2 varianti): iterare su `files[]`.
- `giv video --dry-run` non genera: stampa `{dry_run, provider, model, prompt, prompt_final,
  params, inputs, cost_est_usd, cost_source, warnings}` (`cost_est_usd` null se non stimabile).

## Comandi

```bash
giv models  [--provider P] [--all] [--kind image|video|other] [--caps] [--json]
                                                # elenca modelli (verifica anche l'auth); --caps con --kind video
giv image   [flag] "<prompt>"                   # genera immagini
giv video   [flag] "<prompt>"                   # genera video (job asincrono, 1-5 min)
giv job fetch --provider P [--out D] [--name S] [--timeout 15m] <id>
                                                # scarica un job video già partito, senza nuovo avvio
giv story check <storyboard.json>               # valida uno storyboard e ne stima il costo, gratis
giv story run [--scene <id>] <storyboard.json>  # genera le scene video, a pagamento; riprende da dove si era fermato
giv story cut <storyboard.json>                 # monta con ffmpeg le scene pronte in un solo video, gratis
giv log     [-n N] [--sum-cost [--by provider|model]] [--json]   # registro (giv-log.jsonl nella cwd)
giv presets [--json]                            # preset di camera ed effetti
giv presets show <nome> [<nome>] [--model M] [--prompt "<t>"] [--negative "<t>"]
                 [--tutela prodotto|illustrazione] [--strict]   # frase finale, senza chiamate
giv version
```

`--input`, `--image`, `--last-frame` e `--ref` accettano **anche URL http(s)**: vengono
scaricati da soli (niente curl preventivo). `giv log --sum-cost` dà il totale speso registrato nella directory —
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
`video`: `--aspect <16:9|9:16>`, `--resolution <720p|1080p>`, `--duration <s>`,
`--negative`, `--image <file|url>` (primo fotogramma), `--last-frame <file|url>` (ultimo
fotogramma), `--ref <file|url>` ripetibile (riferimenti di personaggio o prodotto),
`--audio on|off`, `--seed <n>`, `--param k=v` ripetibile (parametro passante, solo se il
modello lo elenca), `--timeout <durata>` (default `15m`), `--preset <nome>` ripetibile (uno
di camera + uno di effetto), `--tutela prodotto|illustrazione|no`, `--dry-run`,
`--max-cost <usd>`, `--strict`.

I 5xx transitori vengono ritentati da soli (2 tentativi con backoff, avviso su stderr):
non serve gestire i retry lato agente. Nei video l'attesa del job riprova anche su 429 ed
errori di rete (fino a 5 volte, rispettando `Retry-After`). La richiesta che **avvia** il
video invece non viene mai ripetuta dopo un 5xx o un timeout: il job potrebbe essere già
partito e un secondo invio costerebbe due video. Se esce con codice 5, **non rilanciare
`giv video`**:

- con l'id del job nel messaggio (il job è partito): `giv job fetch --provider P <id>`
  dalla **stessa directory** (lo stato del job è in `giv-jobs.jsonl` lì). Riprende
  l'attesa e scarica il video senza un nuovo avvio; manifest come `giv video` (`command`
  `job fetch`). Un job fallito dà di nuovo codice 5 con l'id: allora sì, si rigenera.
- senza id (il messaggio dice che il job potrebbe essere partito): controllare sul
  provider prima di rilanciare.

`job fetch` rifiuta con codice 2, senza richieste: gemini e openai (nessun job video), un
id che non è in `giv-jobs.jsonl`, un file di stato che non è un file regolare, un
indirizzo di attesa fuori dall'API del provider. `--timeout` copre anche lo scaricamento.

## Prima di spendere (video)

- `giv video` controlla la richiesta sul modello **prima** di download e generazione:
  openrouter legge `/videos/models` (gratuito), xai usa una tabella interna (20/11/2026).
- **Strutturale** non supportato (`--image`, `--last-frame`, `--ref` o troppi riferimenti,
  `--duration`/`--resolution`/`--aspect` fuori dai valori ammessi, `--audio on` su un
  modello muto, `--param` fuori lista) → **codice 2**, nessuna chiamata; il messaggio
  elenca i modelli dello stesso provider che lo supportano: sceglierne uno di quelli.
- **Rifinitura** non supportata (`--seed`, `--negative`, modello fuori catalogo) →
  avviso su stderr, la generazione parte senza quel controllo. Con `--strict` → codice 2.
- Prima di una clip costosa: `--dry-run` (stima, prompt finale, avvisi; serve la chiave),
  poi la stessa riga senza `--dry-run` e con `--max-cost <usd>` come tetto (oltre, o
  stima impossibile → codice 2).
- `giv models --provider openrouter --kind video --caps` (o `--json`): fotogrammi,
  durate, risoluzioni, audio, parametri passanti e $/s di ogni modello video.

## Matrice provider (default dal codice, modelli da `giv models` del 13/11/2026)

| provider | immagini | video | `--input` | `--aspect` | modelli chiave |
|---|---|---|---|---|---|
| `gemini` | ✅ | ❌ | ✅ | ✅ | immagini `gemini-3.1-flash-image` (default, Nano Banana 2), `gemini-3-pro-image` (top), `gemini-3.1-flash-lite-image`, `gemini-2.5-flash-image`; nessuna API video (Veo spento), usare openrouter |
| `openai` | ✅ | ❌ | ✅ (via edits) | ✅ (mappato su size) | `gpt-image-2.6-flare` (default), `gpt-image-2.6-sunburst`, `gpt-image-2.5-flare`/`-sunburst`, `gpt-image-2`, `gpt-image-1.5`, `gpt-image-1-mini`; nessuna API video |
| `xai` | ✅ | ✅ | ❌ img / ✅ video | ❌ img / ✅ video | `grok-imagine-image-2.0` (default), `grok-imagine-image`; video `grok-imagine-video-1.5` (default, 1-15 s, 480p default, primo e ultimo fotogramma, fino a 14 `--ref`), `grok-imagine-video-1.5-lite` (uguale, più economico), `grok-imagine-video` (solo primo fotogramma, 7 `--ref`); con `--ref` max 720p; `--seed` ignorato; con `--image` ignora `--aspect` |
| `openrouter` | ✅ | ✅ | ✅ | ✅ | immagini `google/gemini-3.1-flash-image` (default), `google/gemini-3-pro-image`, `openai/gpt-image-2.6-flare`, `bytedance-seed/seedream-5-0-lite\|pro`, Flux 2, Recraft v4…; video `google/gemini-omni-1.2-flash` (default), `bytedance/seedance-2.0-mini`, `bytedance/seedance-2.5`, `google/veo-3.1` / `-fast` / `-lite`, `alibaba/wan-3.0` (2-30 s, primo e ultimo fotogramma), Wan 2.x…; capacità per modello con `giv models --kind video --caps` |

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

**Spot da una foto di prodotto** (camera da preset, prodotto fedele):

```bash
./bin/giv video --provider openrouter --model alibaba/wan-3.0 \
  --image foto-prodotto.jpg --preset dolly-in --duration 5 --resolution 720p --audio off \
  --dry-run --name spot-01 "il flacone su un piano di marmo, luce da studio"
# stima ok (0,50 $) → stessa riga senza --dry-run, con --max-cost 0.60
```

Con `--image` la clausola `--tutela prodotto` è già il default (prodotto, etichetta e
proporzioni come nel primo fotogramma). `--last-frame` fissa anche l'inquadratura finale.

**Mascotte coerente fra scene** (la coerenza la porta il fotogramma chiave):

```bash
./bin/giv image --provider openrouter --model google/gemini-3-pro-image \
  --input mascotte-fronte.png --input mascotte-profilo.png --name s2-key \
  "la mascotte apre la porta del magazzino"
./bin/giv video --provider xai --image out/s2-key-<timestamp>.jpg \
  --ref mascotte-fronte.png --ref mascotte-profilo.png --preset static --duration 5 \
  --name s2 "<IMAGE_0> apre la porta e saluta"
```

Una scena per chiamata, riusando le stesse immagini di riferimento; su xai i riferimenti si
richiamano nel prompt con `<IMAGE_0>`, `<IMAGE_1>`…

**Preset di camera ed effetti**:

```bash
./bin/giv presets                                              # elenco (camera ed effetto)
./bin/giv presets show orbit slow-motion --model alibaba/wan-3.0 --prompt "<scena>"
./bin/giv video --provider openrouter --model alibaba/wan-3.0 --image foto.jpg \
  --preset orbit --preset slow-motion --name orbit-01 "<scena>"
```

Al massimo un preset di camera e uno di effetto; le coppie incompatibili escono con codice
2. Varianti scritte per kling, veo, seedance e wan; su grok e omni vale la frase generica
(avviso). I negative dei preset wan su `alibaba/wan-3.0` danno un avviso (il modello non
accetta il negative). Preset propri in `./giv-presets.json` o `~/.config/giv/presets.json`,
stesso formato di `internal/presets/presets.json`.

**Spot a più scene** (storyboard; esempio completo in `examples/spot/storyboard.json`):

1. Una cartella per lo spot: `storyboard.json`, `img/` (foto del prodotto, logo, immagini
   di riferimento), `voce/` (un file audio per scena, vedi la voce fuori campo sotto). I
   percorsi nello storyboard sono relativi a questa cartella e devono restarci dentro.
2. Scrivere lo storyboard: `formato` comune (`aspect`, `risoluzione`, `fps`), blocco
   `default` col modello (`"provider": "openrouter", "modello": "alibaba/wan-3.0",
   "audio": "off"`: clip mute, voce e musica al montaggio), `tetto_costo_usd`, gli
   insiemi di `riferimenti` del prodotto o del personaggio, poi le scene in ordine:
   - `video` con `prompt`, `durata`, un preset di camera, e un solo primo fotogramma fra
     `immagine` (la foto del prodotto), `continua_da` (l'ultimo fotogramma della scena
     prima: la continuità più forte fra due clip) e `fotogramma_chiave` (immagine
     generata prima, coi riferimenti di `usa` come ingressi);
   - `clip` per un video già pronto, `immagine` con `durata` per cartelli e chiusura;
   - `voce` sulle scene con la voce fuori campo. Il tipo `avatar` non c'è in giv 0.9.0.
3. `giv story check storyboard.json` finché non esce con 0. Con codice 2 l'errore elenca
   tutti i problemi, uno per riga. Sul JSON stampato guardare `cost_total_usd` (contro il
   tetto), `not_estimated` (generazioni senza stima: dare un `costo_dichiarato_usd`, per
   esempio ai modelli a token come Seedance e ai fotogrammi chiave) e i `warnings` di ogni
   scena (controlli che il modello ignora). Nessuna generazione, nessun costo.
4. Il modello va scelto fra quelli del catalogo letto in quel momento: un modello sparito
   viene rifiutato (`giv models --provider openrouter --kind video --caps`).
5. Generazione: `giv story run storyboard.json`, a pagamento, solo dopo un check con 0 e
   una stima accettata. Genera le scene video in ordine; clip in `clip/`, stato in
   `storyboard.stato.json` accanto. Se si ferma (timeout, rete, Ctrl-C) **rilanciare lo
   stesso comando**: riprende e recupera i job già partiti senza pagarli due volte. Stati
   nel JSON: `incerto` (richiesta senza esito, forse addebitata: controllare sul provider,
   poi `--scene <id>`), `fallito` (job finito male, spesa contata: rifarla solo con
   `--scene <id>`). Per rifare una scena venuta male: `giv story run --scene s2
   storyboard.json` (la ripresa precedente resta). Il tetto conta tutta la spesa
   registrata nello stato: cancellare lo stato azzera il conto e fa rigenerare tutto, non
   farlo per «liberare» il tetto.
6. Montaggio: `giv story cut storyboard.json` (serve `ffmpeg`), gratis e senza rete,
   quando ogni scena video ha la sua clip nello stato: codice 2 con «clip mancanti» elenca
   le scene ancora da generare. Nel blocco `montaggio`: `transizione` (`taglio` o `dissolvenza` di
   0,5 s), `musica` in loop (`volume` 0,25 se non indicato), `sottotitoli` (`.srt`,
   `.ass`), `uscita` nella cartella dello spot, riscritta a ogni cut. Sul JSON confrontare
   `duration_s` con `expected_duration_s` e leggere i `warnings`, poi la verifica visiva.

**Voce fuori campo** (sintesi locale, nessun costo; giv non sintetizza la voce, riceve i
file già pronti):

1. Un testo breve per scena: l'italiano parlato va a circa 15 caratteri al secondo, una
   scena da 5 s regge una settantina di caratteri.
2. Sintesi con la voce locale del progetto: si fissa una voce di riferimento e si clona la
   stessa voce per tutte le scene, così il timbro non cambia fra una scena e l'altra
   (circa 12 s di calcolo per secondo di audio). Un WAV per scena: `voce/s1.wav`,
   `voce/s2.wav`…
3. Ogni file non più lungo della sua scena:
   `ffprobe -v error -show_entries format=duration -of csv=p=0 voce/s1.wav`
   (`giv story check` lo segnala nei `warnings` della scena).
4. Nello storyboard `"voce": "voce/s1.wav"` sulla scena. I tempi delle frasi per i
   sottotitoli (`montaggio.sottotitoli`) si ricavano dalla trascrizione automatica del WAV.

**Verifica visiva — SEMPRE, dopo ogni generazione**: guardare l'immagine (Read del file);
per i video estrarre 2-3 frame (`ffmpeg -sseof -0.2 -i <mp4> -frames:v 1 /tmp/last.png`)
e controllare: stile coerente, palette giusta, **nessun testo/lettera** indesiderato.

## Costi

- Il costo reale di una chiamata è `cost_usd` nel manifest (openrouter); dove il
  provider non lo riporta (xai) c'è la stima `cost_est_usd`. Il totale registrato è
  `giv log --sum-cost` (`cost_usd` riportato, `cost_est_usd` stimato, `failed_jobs` e
  `failed_cost_est_usd` per i job partiti e poi falliti o scaduti, che possono essere
  stati addebitati); `--by provider|model` divide i totali. Usarli per il budget.
- Prima della chiamata: `giv video … --dry-run` dà `cost_est_usd` (listino × durata) e
  `--max-cost` fa da tetto. Il $/s dei modelli video è in `giv models --kind video --caps`
  (openrouter: `pricing_skus` di `/videos/models`; xai: tabella interna). I modelli a token
  (es. Seedance) non hanno stima: il costo vero è solo nel `cost_usd` dopo la chiamata.
- Il listino immagini è `giv models --provider openrouter --json`, campo `pricing`
  (dollari per token o per unità, come lo riporta il provider). Per gemini, openai e xai
  diretti `models` non riporta prezzi.
- Prezzi video indicativi su openrouter (listino del 20/11/2026, $ al secondo a 720p, tra
  parentesi senza audio): `google/gemini-omni-1.2-flash` 0,07 (audio sempre incluso;
  1080p 0,10), `google/veo-3.1-lite` 0,05 (0,03), `google/veo-3.1-fast` 0,10 (0,08),
  `google/veo-3.1` 0,40 (0,20), `alibaba/wan-3.0` 0,10 (480p 0,05, 1080p 0,20; stesso
  prezzo con o senza audio). Veo e Sora diretti non esistono più (Gemini e OpenAI).
- Per prove ripetute usare i modelli flash/lite/mini e `--duration 4`; il modello top
  solo per l'output finale. `-n N` su openrouter costa N richieste.

## Quirk noti

- **OpenRouter**: `giv models --provider openrouter` elenca l'intero catalogo immagini
  e video (dalla v0.6.0; prima mancavano ByteDance e tutti i video); scheda di un modello
  via `GET /api/v1/models/<id>/endpoints`.
- **HTTP 500 dal provider "Seed"** (ByteDance su OpenRouter): instabilità temporanea
  dell'endpoint, riprovare più tardi — non è un errore del CLI.
- **Kling** non è più nel catalogo video di OpenRouter (21/11/2026): negli esempi
  `alibaba/wan-3.0`. Le varianti kling dei preset restano nella libreria.
- I nomi modello **cambiano spesso** (es. `grok-2-image` → `grok-imagine-image-*`;
  ritirati `veo-3.0-*` e `imagen-4.0-*`; al 13/11/2026 non sono più elencati `gpt-image-1`
  su openai né modelli video su gemini): in caso di 404 sul modello (codice 3), rifare
  `giv models --provider <p>`.
- La clausola "NO text, NO letters" nel prompt va **sempre** tenuta per le illustrazioni:
  tutti i modelli tendono a scrivere etichette.
