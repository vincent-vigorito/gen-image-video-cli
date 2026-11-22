---
name: gen-image-video
description: Generazione di immagini e video via CLI `giv` (gen-image-video-cli) sui provider Gemini, OpenAI, x.ai, OpenRouter (inclusi ByteDance Seedream/Seedance) e fal (Kling, Seedance, Topaz). Usare quando serve generare o modificare immagini da prompt, lavorare con reference di stile (--input), generare video text-to-video o animare un'illustrazione (image-to-video, anche con ultimo fotogramma, immagini di riferimento, fotogrammi intermedi e inquadrature di Kling 4.0, o con Gemini diretto), fare spot da foto di prodotto o mascotte coerenti fra scene con preset di camera ed effetti, controllare capacità e costo di un video prima di spendere (--dry-run, --max-cost), recuperare un job video già partito senza pagarlo due volte (giv job fetch), preparare uno spot a più scene con voce fuori campo da uno storyboard JSON (giv story check, giv story run, giv story cut, anche in più formati con logo, titoli, cartello finale e musica sotto la voce), allungare, modificare o ingrandire una clip esistente (giv extend, giv edit anche sui video Gemini con --from, giv upscale con openrouter o fal), far parlare un personaggio da un'immagine e una voce (giv avatar, fal), rifinire in locale e gratis un video che c'è già (giv post trim, speed, freeze, reframe in 9:16, 1:1 o 4:5, overlay con logo, titoli e cartello finale, mix con musica sotto la voce e volume finale), guardare un video o uno storyboard in un foglio di contatto, estrarne un fotogramma o una GIF (giv sheet, giv frame, giv gif, in locale e gratis), esportare un video finito per Instagram, TikTok, YouTube e LinkedIn (giv export, in locale e gratis), generare la voce fuori campo da un testo (giv voice, xAI con frazioni di centesimo e dizionario delle pronunce, o Gemini), controllare un kit di marca e personaggi (giv kit check, gratis), controllare tecnicamente clip, storyboard e montati prima di consegnare (giv qc: neri, fermi, muti, volume, picco, profili dei canali, in locale e gratis), o confrontare modelli/provider sullo stesso prompt.
---

# giv — CLI generazione immagini e video

## Setup

- Repo: clone di `gen-image-video-cli` — binario `bin/giv`
  (se manca: `make build` dalla radice del repo, richiede Go 1.26+).
- Chiavi e `GIV_PROVIDER` si risolvono in ordine: env di processo → `./credentials.env` →
  `./.env` → `~/.config/giv/credentials.env` (vince il primo che le definisce). L'ultimo
  file rende `giv` usabile da qualsiasi directory, senza spostarsi nel repo.
- Chiavi: `GEMINI_API_KEY`, `OPENAI_API_KEY`, `XAI_API_KEY`, `OPENROUTER_API_KEY`,
  `FAL_KEY` (bastano quelle dei provider che si usano). `GIV_PROVIDER` cambia il provider usato
  senza `--provider` (default `gemini`, solo per le immagini: `giv video` e `giv story` senza
  `--provider`, provider nello storyboard o `GIV_PROVIDER` escono con codice 2).
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
  riportato, es. xai; nei comandi sulle clip resta accanto a `cost_usd`), `cost_usage_usd` + `cost_usage_source` (gemini: costo dai token
  riportati, accanto alla stima), `warnings[]` (controlli ignorati).
- Exit code (il messaggio d'errore API e un `suggerimento:` sono in chiaro su stderr):
  0 ok · 1 errore generico · 2 uso errato (flag, prompt, provider o modello; anche video
  openai, anche con `--model`, e video senza un provider scelto) · 3 modello non trovato (404: `giv models --provider P`) ·
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
giv extend  --video <mp4|url> [--duration 2-10] "<come continua>"   # allunga una clip (xai, a pagamento)
giv extend  --provider gemini --from <job_id> [--duration 3-10] "<come continua>"
                                                # allunga un video fatto con gemini (a pagamento)
giv edit    --video <mp4|url> "<cosa cambiare>"                      # modifica una clip (xai, a pagamento)
giv edit    --provider gemini --from <job_id> "<cosa cambiare>"     # modifica un video gemini (a pagamento)
giv upscale --video <url|file> [--provider openrouter|fal] [--factor 1.5-3|fal 1-4] [--mode precise|creative] ["<prompt>"]
                                                # ingrandisce una clip (file locale solo con fal; openrouter vuole un URL https)
giv avatar  --image <file|url> --audio <wav|mp3> [--model standard|pro] ["<prompt>"]
                                                # personaggio che parla (fal, a pagamento)
giv post <operazione> --video <file> [flag] [--out D] [--name S] [--dry-run]
                                                # rifinitura in locale con ffmpeg, gratis: trim, speed,
                                                # freeze, reframe, overlay, mix (giv post help)
giv sheet   <file|cartella|storyboard.json> [--every <s> | --frames <n>] [--cols <n>] [--labels]
                                                # foglio di contatto JPEG, in locale e gratis
giv frame   --video <file> --at <s> | --last [--width <px>]
                                                # un fotogramma PNG (con --width la miniatura), gratis
giv gif     --video <file> [--start <s>] [--end <s>] [--width <px>] [--fps <n>]
                                                # GIF animata (default 480 px, 12 fps), gratis
giv export  --video <file> --for instagram,tiktok,youtube,linkedin [--fit crop|pad|blur] [--focus <x>[,<y>]] [--loudness <lufs>|off]
                                                # un file per canale col suo profilo, in locale e gratis
giv voice   [--voice eve] [--lang it] [--speed 0.7-1.5] [--pronuncia <f.json>] [--kit <kit.json>]
            [--out D] [--name S] [--dry-run] [--max-cost <usd>] "<testo>" | --text-file <f>
                                                # voce fuori campo WAV + tempi per carattere (xai, a pagamento)
giv voice   --provider gemini [--model gemini-3.8-flash-tts|gemini-3.8-flash-lite-tts] [--voice Kore]
            [--style "<t>"] "<testo>"           # seconda via: solo WAV, niente tempi, mai con --max-cost
giv voice   --voices [--provider xai|gemini]    # elenco delle voci del provider (gratis)
giv kit check <kit.json>                        # controlla il kit di marca e personaggi, gratis, non scrive niente
giv qc      <file|storyboard.json> [--expect-duration <s>] [--expect-size <LxA>] [--expect-fps <n>]
            [--expect-audio on|off] [--for <canali>] [--strict]
                                                # controllo tecnico in locale (neri, fermi, muti, volume, canali), gratis
giv job fetch --provider P [--out D] [--name S] [--timeout 15m] <id>
                                                # scarica un job video già partito, senza nuovo avvio
giv story check <storyboard.json>               # valida uno storyboard e ne stima il costo, gratis
giv story run [--scene <id> [--new-keyframe | --new-job]] <storyboard.json>
                                                # genera le scene video e avatar, a pagamento; riprende da dove si era fermato
giv story cut [--formato <r>] [--export <canali>] <storyboard.json>
                                                # monta con ffmpeg le scene pronte, un file per formato
                                                # di montaggio.formati, gratis; --export come giv export --for
giv story help                                  # aiuto (su stderr); giv story <sottocomando> --help
giv log     [-n N] [--sum-cost [--by provider|model]] [--json]   # registro (giv-log.jsonl nella cwd)
giv presets [--json]                            # preset di camera ed effetti
giv presets show <nome> [<nome>] [--model M] [--prompt "<t>"] [--negative "<t>"]
                 [--tutela prodotto|illustrazione] [--strict]   # frase finale, senza chiamate
giv version
```

`--input`, `--image`, `--last-frame`, `--ref` e `--keyframe` accettano **anche URL http(s)**: vengono
scaricati da soli (niente curl preventivo). `giv log --sum-cost` dà il totale speso registrato nella directory —
usalo per riferire i costi a fine lavoro. Su openrouter `models --json` include il
`pricing` per modello: consultalo per scegliere il motore economico.

Flag comuni: `--provider` (gemini|openai|xai|openrouter|fal, default `GIV_PROVIDER` se
impostato, altrimenti gemini, ma per `giv video` serve un provider scelto; extend, edit,
upscale e avatar hanno il loro: xai, openrouter, fal; con `--provider` anche gemini per
extend ed edit e fal per upscale), `--model` (default per provider: tabella sotto, anche in
`giv --help`), `--out`, `--name`. Il prompt va passato come **singolo argomento
quotato**; i flag possono stare prima o dopo.

`image`: `-n <num>` (su openrouter una richiesta per immagine, costi sommati in
`cost_usd`; se una fallisce le già generate non vengono salvate), `--aspect <1:1|16:9|9:16|4:3|3:4>`, `--seed <n>` (riproducibilità:
gemini e openrouter, altrove ignorato con avviso), `--input <file>` ripetibile
(reference di stile / editing).
`video`: `--aspect <16:9|9:16>`, `--resolution <720p|1080p>`, `--duration <s>`,
`--negative`, `--image <file|url>` (primo fotogramma), `--last-frame <file|url>` (ultimo
fotogramma), `--ref <file|url>` ripetibile (riferimenti di personaggio o prodotto),
`--keyframe <file|url>` ripetibile (fotogrammi intermedi in ordine, fal `kling-4.0`, max 8),
`--shot "<s>:<testo>"` ripetibile (inquadrature, fal `kling-4.0` e `kling-v3-pro`: niente
prompt né `--preset`, la durata è la somma), `--audio on|off`, `--seed <n>`, `--param k=v` ripetibile (parametro passante, solo se il
modello lo elenca), `--timeout <durata>` (default `15m`), `--preset <nome>` ripetibile (uno
di camera + uno di effetto), `--tutela prodotto|illustrazione|no`, `--dry-run`,
`--max-cost <usd>`, `--strict`.
`extend`/`edit`/`upscale`/`avatar`: `--video` (o `--from <job_id>` per extend ed edit
gemini, `--image` e `--audio` per avatar), `--duration` (extend), `--factor` e `--mode`
(upscale; `--mode` e prompt solo openrouter), `--model` (avatar `standard|pro`),
`--dry-run`, `--max-cost`, `--strict`, `--timeout`, `--out`, `--name`; ogni altro flag di
`giv video` è rifiutato con codice 2.
`post <operazione>` (locale, nessuna chiave né spesa): `--video <file>` obbligatorio,
`--out`, `--name`, `--dry-run`; `trim --start --end` · `speed --factor 0.25-4` ·
`freeze --at --duration` · `reframe --aspect 9:16|1:1|4:5|16:9 --fit crop|pad|blur --focus
<x>[,<y>]` · `overlay --logo --logo-pos --logo-width --logo-opacity --logo-from --logo-to
--title --from --duration --pos --end-card --end-color --end-logo --end-text --end-subtext
--end-duration --font --color` · `mix --music --music-volume --duck --voice --voice-at
--loudness -14|off`. JSON con `command` `post <operazione>`, `provider` `local`,
`cost_usd` 0; l'ingresso non si sovrascrive mai (nome preso → `-2`, `-3`…).
`sheet` (locale, stesse regole): file, cartella o storyboard come argomento, `--every <s>`
oppure `--frames 1-100`, `--cols 1-12` (default 4, al più 200 riquadri), `--labels`
(istante; nome del file con una cartella; scena e ripresa con uno storyboard) →
`<nome>-foglio.jpg`. `frame --video <file> --at <s>|--last [--width 16-7680]` →
`<nome>-fotogramma.png`. `gif --video <file> [--start] [--end] (al più 60 s) [--width
16-1920, default 480] [--fps 1-30, default 12]` → `<nome>-gif.gif`.
`export --video <file> --for <canale>[:<formato>],…` (instagram 9:16 Reel, anche 4:5 e
1:1; tiktok 9:16; youtube 16:9, Shorts in 9:16/1:1/4:5; linkedin 16:9) `[--fit] [--focus]
[--loudness -14|off]` → `<nome>-<canale>-<formato>.mp4` per canale; durata fuori dai
limiti del canale = codice 2 prima di scrivere; convenzioni in `exports[].convention`.
`voice` (xai, chiave `XAI_API_KEY`): testo fra virgolette o `--text-file` (al più 15.000
caratteri), voce predefinita `eve`, `--lang it` (BCP-47 o `auto`), `--speed 0.7-1.5`,
`--pronuncia <file.json>` (`{"Swerpify": "suèrpifai"}`, al più 200 parole, parola intera e
senza maiuscole) → `<nome>.wav` (24 kHz) e `<nome>.tempi.json` (`chars[]` con `c`,
`start`, `end`); stima `cost_est_usd` (xai non riporta il costo). Codice 5 = esito
incerto, la voce può essere stata addebitata: **non rilanciare** senza controllare.
`--kit <kit.json>`: voce e pronunce del kit come predefiniti (i flag vincono). Con
`--provider gemini` (chiave `GEMINI_API_KEY`): voce `Kore`, `--style` a parole, lingua dal
testo, `--speed` ignorata, al più 6000 caratteri; pronunce sostituite nel testo, solo il
WAV (sottotitoli distribuiti sulla durata). Il costo Gemini non ha un massimo garantito:
con `--max-cost` (o `tetto_costo_usd`) esce **sempre** con codice 2; per un tetto usare xai.
`kit check <kit.json>`: forma, valori e file del kit (dentro la sua cartella, solo file
normali) con le misure; JSON `files`, `characters`, `voice`, `pronunciations`,
`warnings`; kit non valido = codice 2, un problema per riga.
`qc` (locale, non scrive niente, nemmeno `giv-log.jsonl`): controlli `duration`, `size`,
`fps`, `audio`, `black`, `freeze`, `silence`, `loudness`, `peak`, `streams`, ognuno
`ok`/`avviso`/`difetto`; soglie `--black-min 0.5`, `--freeze-min 2`, `--freeze-noise -60`,
`--silence-min 2`, `--silence-noise -50`; `--for` aggiunge `channels[]` col profilo di ogni
canale; con uno storyboard `scenes[]` e `outputs[]` (`assente` se non ancora fatti; le
`--expect-*` valgono solo su un file); `--strict` = codice 1 con un difetto, senza = 0.

I 5xx transitori vengono ritentati da soli (2 tentativi con backoff, avviso su stderr):
non serve gestire i retry lato agente. Nei video l'attesa del job riprova anche su 429 ed
errori di rete (fino a 5 volte, rispettando `Retry-After`). La richiesta che **avvia** il
video invece non viene mai ripetuta dopo un 5xx o un timeout: il job potrebbe essere già
partito e un secondo invio costerebbe due video. Lo stesso vale per extend, edit,
upscale, avatar e per `giv image` su openrouter (non ripetuta dopo un 5xx: l'immagine può
essere stata pagata). Se esce con codice 5, **non rilanciare `giv video`**:

- con l'id del job nel messaggio (il job è partito): `giv job fetch --provider P <id>`
  dalla **stessa directory** (lo stato del job è in `giv-jobs.jsonl` lì). Riprende
  l'attesa e scarica il video senza un nuovo avvio; manifest come `giv video` (`command`
  `job fetch`). Un job fallito dà di nuovo codice 5 con l'id: allora sì, si rigenera.
- senza id (il messaggio dice che il job potrebbe essere partito): controllare sul
  provider prima di rilanciare.

I job di `giv extend`/`edit` (xai o gemini), `upscale` (openrouter o fal) e `avatar` (fal)
si recuperano allo stesso modo. `job fetch` rifiuta con codice 2, senza richieste: openai
(nessun job video), un
id che non è in `giv-jobs.jsonl`, un file di stato che non è un file regolare, un
indirizzo di attesa fuori dall'API del provider. `--timeout` copre anche lo scaricamento.

## Prima di spendere (video)

- `giv video` controlla la richiesta sul modello **prima** di download e generazione:
  openrouter legge `/videos/models` (gratuito), xai e gemini usano una tabella interna
  (20/11 e 22/11/2026), fal le schede di giv (22/11/2026).
- **Strutturale** non supportato (`--image`, `--last-frame`, `--ref` o troppi riferimenti,
  `--keyframe` o `--shot` fuori da fal `kling-4.0`/`kling-v3-pro`,
  `--duration`/`--resolution`/`--aspect` fuori dai valori ammessi, `--audio on` su un
  modello muto, `--param` fuori lista) → **codice 2**, nessuna chiamata; il messaggio
  elenca i modelli dello stesso provider che lo supportano: sceglierne uno di quelli.
- Via openrouter i modelli `x-ai/…` non accettano `--image` e `--ref` insieme → codice 2
  prima della chiamata: usarne uno solo, oppure `--provider xai` (li accetta).
- **Rifinitura** non supportata (`--seed`, `--negative`, modello fuori catalogo) →
  avviso su stderr, la generazione parte senza quel controllo. Con `--strict` → codice 2.
- Prima di una clip costosa: `--dry-run` (stima, prompt finale, avvisi; serve la chiave),
  poi la stessa riga senza `--dry-run` e con `--max-cost <usd>` come tetto (oltre, o
  stima impossibile → codice 2). Con `--max-cost` indicare `--audio` e `--resolution`: se
  il listino non copre una configurazione possibile la stima non basta e il codice 2 dice
  quale flag manca (es. `google/gemini-omni-1.2-flash` vuole `--audio on`). Nei comandi
  sulle clip senza misura del sorgente (URL, ffprobe assente; per gemini un `--from` che
  non è in `giv-log.jsonl` della directory) la stima non c'è: con `--max-cost` codice 2.
- `giv models --provider openrouter --kind video --caps` (o `--json`): fotogrammi,
  durate, risoluzioni, audio, parametri passanti e $/s di ogni modello video.

## Matrice provider (default dal codice, modelli da `giv models` del 13/11/2026)

| provider | immagini | video | `--input` | `--aspect` | modelli chiave |
|---|---|---|---|---|---|
| `gemini` | ✅ | ✅ | ✅ | ✅ | immagini `gemini-3.1-flash-image` (default, Nano Banana 2), `gemini-3-pro-image` (top), `gemini-3.1-flash-lite-image`, `gemini-2.5-flash-image`; video `gemini-omni-1.2-flash` diretto (solo con provider scelto, 3-15 s default 10, 360p-4k default 720p, 16:9 o 9:16, audio sempre, `--last-frame` dopo `--image` e max 10 s, fino a 6 `--ref` senza `--image`), extend ed edit dei suoi video con `--from` |
| `openai` | ✅ | ❌ | ✅ (via edits) | ✅ (mappato su size) | `gpt-image-2.6-flare` (default), `gpt-image-2.6-sunburst`, `gpt-image-2.5-flare`/`-sunburst`, `gpt-image-2`, `gpt-image-1.5`, `gpt-image-1-mini`; nessuna API video |
| `xai` | ✅ | ✅ | ❌ img / ✅ video | ❌ img / ✅ video | `grok-imagine-image-2.0` (default), `grok-imagine-image`; video `grok-imagine-video-1.5` (default, 1-15 s, 480p default, primo e ultimo fotogramma, fino a 14 `--ref`), `grok-imagine-video-1.5-lite` (uguale, più economico), `grok-imagine-video` (solo primo fotogramma, 7 `--ref`); con `--ref` max 720p; `--seed` ignorato; con `--image` ignora `--aspect` |
| `openrouter` | ✅ | ✅ | ✅ | ✅ | immagini `google/gemini-3.1-flash-image` (default), `google/gemini-3-pro-image`, `openai/gpt-image-2.6-flare`, `bytedance-seed/seedream-5-0-lite\|pro`, Flux 2, Recraft v4…; video `google/gemini-omni-1.2-flash` (default), `bytedance/seedance-2.0-mini`, `bytedance/seedance-2.5`, `google/veo-3.1` / `-fast` / `-lite`, `alibaba/wan-3.0` (2-30 s, primo e ultimo fotogramma), Wan 2.x…; capacità per modello con `giv models --kind video --caps` |
| `fal` | ❌ | ✅ | ❌ | ❌ Kling / ✅ Seedance | video `kling-v3-pro` (default, Kling 3 Pro image-to-video: `--image` obbligatorio, 3-15 s, primo e ultimo fotogramma, 2-4 `--ref`, `--shot`, niente `--resolution`/`--aspect`), `kling-4.0` (`--image` obbligatorio, 3-30 s, 720p/1080p default/4k, fino a 8 `--keyframe`, `--shot`, niente `--aspect`/`--audio`/`--ref`), `seedance-2.0-ref` (1-9 `--ref` e prompt obbligatori, 4-15 s, 480p/720p/1080p); avatar `standard`/`pro` con `giv avatar`; ingrandimento `topaz-upscale` con `giv upscale --provider fal`; schede fissate in giv, non un catalogo |

- **Video gemini**: `gemini-omni-1.2-flash` diretto, a pagamento, parte solo con una scelta
  esplicita (`--provider gemini`, provider dello storyboard o `GIV_PROVIDER`); senza, codice
  2 come prima, così un comando vecchio non comincia a spendere. `--audio off` è rifiutato
  (l'audio c'è sempre: guidarlo nel prompt, es. "No dialogue"). Job recuperabile con
  `giv job fetch --provider gemini <id>`. Veo resta su openrouter: `google/veo-3.1`,
  `-fast`, `-lite` (durate 4|6|8); sulla Gemini API è spento dal 22/10/2026.
- **Video openai**: non supportato (Sora spento da OpenAI il 24/09/2026, rimosso nella 0.7.0): `giv video --provider openai`
  esce subito con codice 2, senza chiamate di rete. Usare `--provider openrouter` o `xai`.
- **fal**: chiave `FAL_KEY`; `giv models --provider fal --kind video --caps` elenca le
  schede di giv; immagini e voci locali vanno sullo storage di fal (scadenza un'ora).
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
  --ref mascotte-fronte.png --ref mascotte-profilo.png --preset static --tutela illustrazione \
  --duration 5 --name s2 "<IMAGE_0> apre la porta e saluta"
```

Una scena per chiamata, riusando le stesse immagini di riferimento; su xai i riferimenti si
richiamano nel prompt con `<IMAGE_0>`, `<IMAGE_1>`… Con `--image` e un preset la clausola
di default è `prodotto`: per una mascotte `--tutela illustrazione`.

**Preset di camera ed effetti**:

```bash
./bin/giv presets                                              # elenco (camera ed effetto)
./bin/giv presets show orbit slow-motion --model alibaba/wan-3.0 --prompt "<scena>"
./bin/giv video --provider openrouter --model alibaba/wan-3.0 --image foto.jpg \
  --preset orbit --preset slow-motion --name orbit-01 "<scena>"
```

Al massimo un preset di camera e uno di effetto; le coppie incompatibili escono con codice
2. Varianti scritte per kling, veo, seedance, grok (`grok-imagine-video-*` su xai) e wan;
su omni vale la frase generica (avviso). I negative dei preset wan su `alibaba/wan-3.0` danno un avviso (il modello non
accetta il negative). Preset propri in `./giv-presets.json` o `~/.config/giv/presets.json`,
stesso formato di `internal/presets/presets.json`.

**Allungare o modificare una clip** (xai, `grok-imagine-video`):

```bash
./bin/giv extend --video out/s1.mp4 --duration 6 --dry-run --max-cost 1.20 \
  "la camera si allontana e mostra il negozio"
./bin/giv edit --video out/s1.mp4 --max-cost 0.80 "aggiungi la pioggia, stessa inquadratura"
```

Sorgente mp4 locale (max 50 MiB, caricato su xAI) o URL `https` che finisce in `.mp4`.
extend: sorgente 2-15 s, aggiunge 2-10 s (default 6); edit: sorgente fino a 8,7 s. Stima
come la fatturazione vista nella prova del 22/11/2026: extend = secondi aggiunti × 0,07 +
sorgente (secondi interi per eccesso) × 0,01 $/s (3 s su una clip di 6 s: 0,27 $), edit =
sorgente × 0,08 $/s (0,48 $ su 6 s); il costo vero è `cost_usd` a fine job, con la stima
accanto. Senza ffprobe la durata non si misura: niente stima, e con `--max-cost` codice 2.

**Allungare o modificare un video Gemini** (`--from`, dall'UE solo video generati da Gemini):

```bash
./bin/giv video --provider gemini --duration 8 --max-cost 1.50 "un faro sulla scogliera al tramonto"
./bin/giv extend --provider gemini --from <job_id> --duration 6 --dry-run "la camera sale sopra il faro"
./bin/giv edit --provider gemini --from <job_id> --max-cost 2.00 "aggiungi la nebbia"
```

`--from` è il `job_id` del manifest di un video gemini (anche di un extend o edit), `--video`
è rifiutato. extend aggiunge 3-10 s (default 10) fino a 30 s in tutto; edit fino a 15 s.
Durata e risoluzione di partenza vengono da `giv-log.jsonl`: lanciare dalla stessa
directory del `giv video`, altrimenti niente stima (con `--max-cost` codice 2).

**Ingrandire una clip** (openrouter, `black-forest-labs/flux-video-upscale`):

```bash
./bin/giv upscale --video https://example.com/clip.mp4 --factor 2 --dry-run
./bin/giv upscale --video https://example.com/clip.mp4 --factor 2 --mode precise
```

`--factor` 1,5-3 (default 2), `--mode precise` (default) o `creative`, prompt facoltativo.
Solo un URL `https` pubblico: un file locale esce con codice 2 e il messaggio propone
`--provider fal` (nella prova del 22/11/2026 OpenRouter ha rifiutato il file come data URI,
HTTP 400, senza addebito). Da URL giv non misura il sorgente: niente stima, quindi niente
`--max-cost` (codice 2); listino 0,035 $ (precise) o 0,05 $ (creative) al megapixel-secondo,
il costo vero è `cost_usd` a fine job. Per un file locale usare fal (sotto).

**Ingrandire un file locale** (fal, Topaz `topaz-upscale`; serve `FAL_KEY`):

```bash
./bin/giv upscale --provider fal --video out/s1.mp4 --factor 2 --dry-run
./bin/giv upscale --provider fal --video out/s1.mp4 --factor 2 --max-cost 0.50
```

Solo `--video` (mp4 locale fino a 50 MiB, caricato sullo storage di fal, o URL `https`) e
`--factor` 1-4 (default 2); `--mode` e prompt rifiutati. Stima per secondo del sorgente e
fascia d'uscita (lati × fattore): 0,01 $/s fino a 720p, 0,02 fino a 1080p, 0,08 oltre, il
doppio sopra i 30 fps; servono durata, lati e fps da ffprobe (da URL niente stima).

**Kling 4.0 con fotogrammi intermedi e inquadrature** (fal; serve `FAL_KEY`):

```bash
./bin/giv video --provider fal --model kling-4.0 --image img/inizio.png \
  --keyframe img/meta.png --last-frame img/fine.png --duration 8 --dry-run "…"
./bin/giv video --provider fal --model kling-4.0 --image img/prodotto.png \
  --shot "3:primo piano del flacone sul marmo" --shot "4:la camera arretra e mostra il bagno" \
  --max-cost 2.00
```

`--image` obbligatorio; 3-30 s, 720p/1080p (default)/4k, niente `--aspect`, `--audio`,
`--ref`. `--keyframe` fino a 8, in ordine. Con `--shot` niente prompt né `--preset`: il
testo sta nelle inquadrature (1-30 s, fino a 512 caratteri ciascuna) e il video dura la
somma. `--shot` vale anche con `kling-v3-pro` (1-15 s, fino a 2500 caratteri).

**Personaggio che parla** (fal, Kling AI Avatar v2; serve `FAL_KEY`):

```bash
./bin/giv avatar --image anja.png --audio voce/saluto.wav --dry-run "sorride e parla in camera"
./bin/giv avatar --image anja.png --audio voce/saluto.wav --model standard --max-cost 1.00 \
  "sorride e parla in camera"
```

La clip dura quanto l'audio (wav o mp3 locale, max 25 MiB) e ne porta la voce: preparare
prima la voce (vedi la voce fuori campo sotto). `standard` 0,0562 $/s, `pro` 0,115 $/s sui
secondi dell'audio arrotondati per eccesso; oltre 60 s un avviso. Il costo resta stimato
(`cost_est_usd`): fal non lo riporta.

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
     generata prima, coi riferimenti di `usa` come ingressi); con fal `kling-4.0` anche
     `fotogrammi_intermedi` (come `--keyframe`) e con `kling-4.0` o `kling-v3-pro`
     `inquadrature` (`[{"prompt": "…", "durata": 3}, …]`, come `--shot`: niente `prompt`
     né `preset`, la scena dura la somma);
   - il provider va scelto: nel blocco `default`, nella scena o con `GIV_PROVIDER`;
     senza, codice 2;
   - `clip` per un video già pronto, `immagine` con `durata` per cartelli e chiusura;
   - `voce` sulle scene con la voce fuori campo; `ritardo_voce` (secondi) la fa partire
     dopo l'inizio della scena;
   - per il montaggio, facoltativi: `fuoco` (0-1, o `{"x": …, "y": …}`: il punto da
     tenere quando un altro formato ritaglia la scena), `taglio` (`{"da": 0.5, "a": 4}`,
     il pezzo della clip da usare) e `velocita` (0.25-4); `durata` resta quella chiesta
     al modello, quindi la stima non cambia;
   - `avatar` per un personaggio che parla in scena (fal): `immagine` e `voce`
     obbligatorie, `prompt`, `modello` `standard` o `pro` (default nel blocco
     `"avatar": {"modello": …}`); dura quanto la voce, niente `durata` né `ritardo_voce`.
   - `tutela` nel blocco `default` o nella scena: senza indicazione, una scena con preset
     e primo fotogramma prende la clausola `prodotto` (etichetta e proporzioni del
     prodotto). Per una **mascotte** o un'illustrazione mettere `"tutela":
     "illustrazione"` (stile piatto, colori e composizione, niente testo); `"no"` toglie
     la clausola. Un valore sconosciuto esce con codice 2.
3. `giv story check storyboard.json` finché non esce con 0. Con codice 2 l'errore elenca
   tutti i problemi, uno per riga. Sul JSON stampato (stdout solo JSON, avvisi su
   stderr) guardare `cost_total_usd` (contro il tetto), `not_estimated` (generazioni senza
   stima: dare un `costo_dichiarato_usd`, per esempio ai modelli a token come Seedance e
   ai fotogrammi chiave), `removed_scenes`/`removed_spent_usd` (spesa già registrata nello
   stato per scene tolte dallo storyboard: conta nel tetto) e i `warnings` di ogni scena
   in `scenes` (controlli che il modello ignora). Nessuna generazione, nessun costo.
4. Il modello va scelto fra quelli del catalogo letto in quel momento: un modello sparito
   viene rifiutato (`giv models --provider openrouter --kind video --caps`).
5. Generazione: `giv story run storyboard.json`, a pagamento, solo dopo un check con 0 e
   una stima accettata. Genera le scene video in ordine; clip in `clip/`, stato in
   `storyboard.stato.json` accanto. Se si ferma (timeout, rete, Ctrl-C) **rilanciare lo
   stesso comando**: riprende e recupera i job già partiti senza pagarli due volte. Stati
   nel JSON: `incerto` (richiesta senza esito, forse addebitata: controllare sul provider,
   poi `--scene <id>`), `fallito` (job finito male, spesa contata: rifarla solo con
   `--scene <id>`). Per rifare una scena venuta male: `giv story run --scene s2
   storyboard.json` (la ripresa precedente resta; il fotogramma chiave già generato si
   riusa). Il tetto conta tutta la spesa registrata nello stato, anche quella di scene
   tolte dallo storyboard (a parte in `removed_scenes`): cancellare lo stato azzera il
   conto e fa rigenerare tutto, non farlo per «liberare» il tetto.
   **Job partito che non si scarica più** (scaduto sul provider, risultato perso): `giv
   story run --scene s2 --new-job storyboard.json` lo abbandona (il suo addebito resta nel
   tetto, il job fra le riprese precedenti) e ne avvia uno nuovo. Prima provare il
   recupero normale: rilanciare lo stesso comando senza flag.
   **Scena col fotogramma chiave sbagliato** (personaggio diverso, prodotto deformato): la
   clip nasce da lì, quindi rifare solo la clip non basta. Correggere se serve il
   `prompt` o gli ingressi (`usa`) del `fotogramma_chiave`, `giv story check`, poi
   `giv story run --scene s2 --new-keyframe storyboard.json`: rigenera a pagamento il
   fotogramma e la clip, dentro il tetto; il fotogramma e la clip precedenti restano nello
   stato e su disco. Il fotogramma chiave non si rigenera mai da solo: se il suo file è
   stato cancellato la scena esce con codice 2 e chiede `--new-keyframe`. Guardare il
   nuovo fotogramma (`keyframe` nel JSON) prima del montaggio.
6. Montaggio: `giv story cut storyboard.json` (serve `ffmpeg`), gratis e senza rete,
   quando ogni scena video ha la sua clip nello stato: codice 2 con «clip mancanti» elenca
   le scene ancora da generare. Nel blocco `montaggio`: `transizione` (`taglio` o `dissolvenza` di
   0,5 s), `musica` in loop (`volume` 0,25 se non indicato), `sottotitoli` (`.srt`,
   `.ass`), `uscita` nella cartella dello spot, riscritta a ogni cut. Sul JSON confrontare
   `duration_s` con `expected_duration_s` e leggere i `warnings`, poi la verifica visiva.
   Dalla 0.11.0, tutti facoltativi (senza, il montaggio di prima):
   - `formati` (`["16:9", "9:16", "1:1", "4:5"]`, nessuno predefinito): un file per
     formato, `spot-9x16.mp4`…; `adatta` `bande` (default), `ritaglio` (col `fuoco` della
     scena) o `sfocato`; `--formato 9:16` ne monta uno solo;
   - `logo` (`file`, `posizione` default `alto-destra`, `larghezza` 0.12, `opacita`, `da`,
     `a`), `titoli` (`[{"testo": …, "scena": "s1", "da": 0.5, "durata": 3, "posizione":
     "basso"}]`, tempi dall'inizio della scena), `cartello_finale` (`durata` 3, `sfondo`
     `#RRGGBB`, `logo`, `testo`, `sottotesto`, accodato muto: la musica continua),
     `stile_testi` (`font` nella cartella, `colore`, `bordo` o `"nessuno"`,
     `dimensione`), tutti nei margini sicuri del formato e coi testi alla lettera;
   - `musica.sotto_voce: true` (la abbassa sotto le voci), `musica.sfuma_fine` (secondi),
     `volume` finale in LUFS (es. -14);
   - `--export instagram,tiktok,youtube` dopo il montaggio un file per canale dal montato
     nel formato del canale (`spot-instagram-9x16.mp4`; il formato deve essere fra quelli
     montati);
   - `giv story run` e `giv story cut` riscrivono `foglio-contatto.jpg` accanto allo
     storyboard (una riga per scena): guardarlo per primo; `"foglio_contatto": false`
     per non farlo.

**Voce fuori campo** (`giv voice`, xAI: 15 $ per milione di caratteri, uno spot da 30 s
costa meno di un centesimo):

1. Un testo breve per scena: l'italiano parlato va a circa 15 caratteri al secondo, una
   scena da 5 s regge una settantina di caratteri.
2. Una voce per scena, sempre la stessa (`eve`, predefinita), così il timbro non cambia
   fra una scena e l'altra; i nomi propri con il dizionario delle pronunce:

   ```bash
   ./bin/giv voice --dry-run --pronuncia pronunce.json "Swerpify ti porta dove vuoi."   # stima
   ./bin/giv voice --pronuncia pronunce.json --out voce --name s1 --max-cost 0.01 \
     "Swerpify ti porta dove vuoi."            # → voce/s1.wav e voce/s1.tempi.json
   ```

   Ascoltare il WAV prima di montare; dopo un codice 5 non rilanciare alla cieca.
3. Ogni voce deve finire prima che parta quella della scena successiva:
   `ffprobe -v error -show_entries format=duration -of csv=p=0 voce/s1.wav`.
   `giv story check` lo controlla sulla linea del tempo del montaggio (durate,
   dissolvenze, `ritardo_voce`) e nei `warnings` della scena dice di quanti secondi si
   sovrappone e a quale scena; una voce che sfora la sua scena senza toccarne un'altra va
   bene.
4. Nello storyboard `"voce": "voce/s1.wav"` sulla scena, e `"ritardo_voce": 1.5` per
   farla partire dopo. I tempi delle frasi per i sottotitoli (`montaggio.sottotitoli`) si
   ricavano da `voce/s1.tempi.json` (tempi per carattere), a partire dall'inizio della
   scena: `giv story cut` sposta da solo le righe del `ritardo_voce`.

**Rifinire uno spot in locale** (gratis, nessuna chiamata; ogni passo scrive un file nuovo
in `--out` e ne dà il percorso in `files[0].path` del JSON):

```bash
./bin/giv post trim    --video out/spot.mp4 --start 0.4 --name spot-a          # via l'inizio fermo
./bin/giv post reframe --video out/spot-a.mp4 --aspect 9:16 --focus 0.45       # → out/spot-a-9x16.mp4
./bin/giv post overlay --video out/spot-a-9x16.mp4 --logo img/logo.png \
  --title "Crema alle erbe" --from 1 --duration 3 \
  --end-color "#1E3A2B" --end-logo img/logo.png --end-text "Swerpify" --end-subtext "swerpify.it"
./bin/giv post mix     --video out/spot-a-9x16-overlay.mp4 --music musica.mp3 --duck \
  --voice voce.wav --voice-at 0.5                                               # volume finale -14 LUFS
```

- Prima `--dry-run` se serve vedere il piano: stessi controlli, nessun file.
- `reframe` ha l'inquadratura fissa: controllare i fotogrammi (il soggetto può uscire dal
  quadro) e spostare `--focus`, o usare `--fit blur`.
- `overlay` tiene logo e titolo dentro i margini sicuri del formato (9:16: 14% alto, 35%
  basso, 6% lati); i testi sono resi alla lettera (niente tag `.ass`). Il cartello finale
  allunga il video di `--end-duration` (default 3 s) con audio muto: per una musica che
  copra anche il cartello fare `mix` **dopo** `overlay`.
- `mix --loudness -14` è una convenzione dei social, non un requisito ufficiale.
- Per più canali in un colpo, sul video finito: `giv export --video out/spot.mp4 --for
  instagram,tiktok,youtube,linkedin` (cambio formato, fps e volume del canale per ogni file).

**Verifica visiva — SEMPRE, dopo ogni generazione**: guardare l'immagine (Read del file);
per i video guardare un foglio di contatto (`giv sheet out/clip.mp4 --frames 8 --labels`,
o `giv sheet storyboard.json --labels` per tutte le scene) o l'ultimo fotogramma
(`giv frame --video out/clip.mp4 --last`) e controllare: stile coerente, palette giusta, **nessun testo/lettera** indesiderato.

**Controllo tecnico prima di consegnare** (gratis, in locale; avvisa e basta, non corregge
e non rigenera):

```bash
./bin/giv qc out/clip.mp4 --expect-duration 5 --expect-audio off   # una clip
./bin/giv qc storyboard.json                                        # clip in uso e montati
./bin/giv qc out/spot-9x16.mp4 --for instagram,tiktok --strict      # profilo dei canali
```

- `difetto` (troppo corta, nera o ferma o muta per metà della durata, misure o fps
  sbagliati, picco sopra 0 dBTP): rigenerare è a pagamento, va chiesto (`giv story run
  --scene <id> --new-job`). `avviso`: valutare guardando il foglio di contatto.
- Una ripresa quasi ferma sfugge alla soglia predefinita: `--freeze-noise -50`.
- Gli avvisi di canale (codec, indice `moov`, volume, misure dello stesso formato) li
  sistema `giv export`.

**Kit di marca e personaggi**: prima di usarlo `./bin/giv kit check kit.json` (gratis):
file con le misure, avvisi su logo senza trasparenza, font, riferimenti e descrizione;
codice 2 con un problema per riga se qualcosa non va. Esempio in `examples/kit/`.

## Costi

- Il costo reale di una chiamata è `cost_usd` nel manifest (openrouter); dove il
  provider non lo riporta (xai) c'è la stima `cost_est_usd`. Il totale registrato è
  `giv log --sum-cost` (`cost_usd` riportato, `cost_est_usd` stimato delle sole righe
  senza costo riportato, `failed_jobs` e
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
- Su fal (schede di giv al 22/11/2026, da confermare): `kling-v3-pro` 0,168 $/s con audio
  e 0,14 senza; `seedance-2.0-ref` 0,014 $ ogni 1000 token (token = larghezza × altezza ×
  secondi × 24 / 1024); avatar `standard` 0,0562 $/s e `pro` 0,115 $/s. Fal non riporta
  il costo: nel manifest c'è la stima.
- Su fal anche `kling-4.0` 0,157 $/s a 720p, 0,235 a 1080p, 0,549 in 4k (senza audio) e
  l'upscale `topaz-upscale` a 0,01/0,02/0,08 $ al secondo per fascia d'uscita (×2 sopra
  i 30 fps).
- Gemini diretto (`gemini-omni-1.2-flash`) si paga a token: la stima è un massimo, coi
  secondi d'uscita (0,0695 $/s a 720p, 0,1043 a 1080p, 0,2085 in 4k, 0,0232 a 360p) più
  ingresso al limite con immagini e il massimo del testo di ragionamento (circa 0,59 $). A
  fine job `cost_usage_usd` dà il costo dai token riportati.
- Comandi sulle clip: extend xai stimato su secondi aggiunti più ingresso ed edit xai sul
  sorgente, con il costo vero di xAI in `cost_usd` a fine job e la stima accanto; extend
  ed edit gemini come `giv video` gemini; upscale openrouter senza stima (da URL), con
  `cost_usd` di openrouter; upscale fal a secondi per fascia, solo stima.
- Voce: xai 15 $ per milione di caratteri del testo inviato (le pronunce non contano);
  stima `cost_est_usd` nel manifest, xai non riporta il costo. Gemini 0,50 $ per milione
  di token di testo e 9 $ (flash-lite 6 $) per milione di token audio (25 al secondo), il
  doppio dal 01/01/2027: stima *probabile* di circa 0,15 $ a richiesta, costo dai token in
  `cost_usage_usd`. Kit e `giv qc` gratis.
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
- **File di uscita mai sovrascritti**: se il nome c'è già il nuovo file prende `-2`, `-3`…
  Leggere sempre il percorso dal manifest.
- **Scaricamenti**: i risultati fuori dall'API del provider si scaricano solo da `https`
  verso indirizzi pubblici, con connessione diretta (senza proxy): se fallisce, il codice
  5 riporta l'id del job e `giv job fetch` lo riprende senza un nuovo avvio.
- **Montaggio e scene avatar** vogliono una cartella temporanea protetta: con `TMPDIR`
  scrivibile da altri senza sticky bit si fermano; impostare `TMPDIR` su una cartella
  propria.
- La clausola "NO text, NO letters" nel prompt va **sempre** tenuta per le illustrazioni:
  tutti i modelli tendono a scrivere etichette.
