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
- **exit code parlanti** — l'agente decide dal codice cosa fare; il messaggio API e un
  `suggerimento:` stanno in chiaro su stderr:

  | Codice | Significato | Cosa fare |
  |---|---|---|
  | 0 | successo | — |
  | 1 | errore runtime generico | leggere il messaggio su stderr |
  | 2 | uso errato (flag, prompt, provider o modello non validi) | correggere la chiamata |
  | 3 | modello non trovato (HTTP 404) | `giv models --provider <p>` e scegliere un altro modello |
  | 4 | autenticazione o credito (HTTP 401/402/403) | controllare la chiave, ricaricare il credito o cambiare provider |
  | 5 | errore dell'API o di rete | riprovare; per i video leggere l'id del job nel messaggio e scaricarlo con `giv job fetch` |
  | 7 | troppe richieste (HTTP 429) | aspettare e riprovare |
  | 10 | chiave API mancante | impostare la chiave del provider |
- **manifest completo** — path dei file scritti, **mime reale** restituito dal provider
  (non dedotto dall'estensione), byte, e `cost_usd` quando il provider riporta il costo
  (OpenRouter, immagini e video): un agente con un budget sa quanto ha speso. Campi in
  più, presenti solo quando servono: `command`, `prompt_final` (prompt inviato coi
  preset), `presets[]`, `inputs` (file per ruolo: `image`, `last_frame`, `refs[]`, con
  impronta sha256 breve; gli URL ridotti a schema e host), `params`, `job_id`,
  `duration_s` (durata reale del video), `cost_est_usd` e `cost_source` (stima da listino
  quando il provider non riporta il costo, es. xai), `warnings[]`.
- **naming deterministico** — `<out>/<slug>-<timestamp>[-n].<ext>`; `--name` per slug
  stabili, `--out` per la directory.
- **credenziali senza interazione** — env di processo → `./credentials.env` → `./.env` →
  `~/.config/giv/credentials.env` (così `giv` funziona da qualsiasi directory); niente
  login, niente browser. Lo stesso vale per `GIV_PROVIDER`, il provider usato senza
  `--provider` (default `gemini`).
- **retry automatico sui 5xx transitori** — fino a 2 ritentativi con backoff (avviso su
  stderr): un blip del provider non fa fallire il task dell'agente.
- **attesa dei video che riprova** — durante il polling del job, 5xx, 429 ed errori di
  rete sono ritentati (fino a 5 volte, con `Retry-After` quando c'è): un disturbo
  passeggero non butta un video già avviato. Se l'errore resta, il messaggio riporta
  l'**id del job** per ritrovarlo sul provider.
- **creazione del job video non ripetuta** — la richiesta che avvia il video non viene
  mai rilanciata da sola dopo un 5xx o un timeout (il job potrebbe essere partito e un
  secondo invio costarebbe due video): `giv` esce con codice 5 e un suggerimento.
- **recupero dei job** — ogni job video avviato lascia il suo stato in `giv-jobs.jsonl`;
  `giv job fetch --provider P <id>` ne riprende l'attesa e scarica il video **senza un
  nuovo avvio** (vedi [Recupero dei job video](#recupero-dei-job-video)).
- **storyboard a più scene** — `giv story check` valida un file JSON con le scene di uno
  spot (modello, file, preset, riferimenti fra scene) e ne stima il costo senza generare
  niente (vedi [Storyboard](#storyboard-video-a-più-scene)).
- **capacità e costo prima di spendere** — prima di avviare un video `giv` controlla il
  modello (OpenRouter: `/videos/models`; xAI: tabella interna). Un controllo che il
  modello non supporta (fotogrammi, riferimenti, durata, risoluzione, aspect, audio,
  `--param` fuori lista) esce con **codice 2 prima di qualsiasi chiamata** e indica i
  modelli dello stesso provider che lo supportano. `--dry-run` stampa parametri, avvisi e
  **costo stimato** senza generare; `--max-cost` rifiuta oltre un tetto in dollari.
- **preset di camera ed effetti** — `--preset dolly-in`, `--preset slow-motion`…: frasi
  scritte per famiglia di modello, aggiunte al prompt (nessuna API video ha un parametro
  di camera); `giv presets` le elenca e le mostra senza chiamate.
- **riproducibilità** — `--seed` dove il provider lo supporta (immagini: Gemini,
  OpenRouter; video: OpenRouter sui modelli che lo dichiarano).
- **registro locale** — ogni generazione è appesa a `giv-log.jsonl` nella directory
  corrente, e anche ogni **job video partito e poi fallito o scaduto** (`status`
  `failed`/`timeout`, `job_id`), perché può essere stato addebitato. `giv log` la storia;
  `giv log --sum-cost` il totale riportato (`cost_usd`) e quello stimato
  (`cost_est_usd`), più i job falliti (`failed_jobs`, `failed_cost_est_usd`); con
  `--by provider|model` anche i totali per gruppo.
- **input da URL** — `--input`, `--image`, `--last-frame` e `--ref` accettano anche URL
  http(s), scaricati da soli.
- **prezzi interrogabili** — `giv models --provider openrouter --json` include il listino
  per modello; `giv models --kind video --caps` dà capacità e prezzo al secondo dei
  modelli video (xai, openrouter): un agente può scegliere il motore economico senza
  tabelle hardcoded.
- **skill inclusa** — `skills/gen-image-video/SKILL.md`: guida operativa per agenti con
  matrice provider, ricette e quirk noti.

## Quickstart

Richiede Go 1.26+.

```bash
make build                # → bin/giv
cp .env.example .env      # inserisci le chiavi dei provider che usi
./bin/giv models          # elenca i modelli (e verifica l'auth)
./bin/giv image "a red panda astronaut, flat illustration"
```

Per usare `giv` da qualsiasi directory metti le chiavi (e, se vuoi, `GIV_PROVIDER`) in
`~/.config/giv/credentials.env`.

## Comandi

```bash
giv models  [--provider P] [--all] [--kind image|video|other] [--caps] [--json]
                                              # modelli disponibili; --caps (con --kind video): capacità e $/s
giv image   [flag] "<prompt>"                 # genera immagini
giv video   [flag] "<prompt>"                 # genera video (job asincrono, 1-5 min)
giv job fetch --provider P [--out D] [--name S] [--timeout 15m] <id>
                                              # scarica un job video già partito, senza nuovo avvio
giv story check <storyboard.json>             # valida uno storyboard a più scene e ne stima il costo
giv log     [-n N] [--sum-cost [--by provider|model]] [--json]   # registro locale delle generazioni
giv presets [--json]                          # preset di camera ed effetti
giv presets show <nome> [<nome>] [--model M] [--prompt "<t>"] [--negative "<t>"]
                 [--tutela prodotto|illustrazione] [--strict]
                                              # frase finale dei preset per quel modello, senza chiamate
giv version
```

Flag comuni: `--provider` (gemini | openai | xai | openrouter; senza flag vale
`GIV_PROVIDER`, altrimenti gemini), `--model` (default per provider, vedi sotto),
`--out`, `--name`. Il prompt è un **singolo argomento quotato**; i flag possono stare
prima o dopo.

| Comando | Flag specifici |
|---|---|
| `image` | `-n <num>` (su OpenRouter una richiesta per immagine, costi sommati) · `--aspect <1:1\|16:9\|9:16\|4:3\|3:4>` · `--seed <n>` · `--input <file\|url>` (ripetibile: reference di stile / editing) |
| `video` | tabella sotto |
| `job fetch` | `--provider` (xai o openrouter, quello del job) · `--out` · `--name` · `--timeout <durata>` (default `15m`); l'id del job è l'unico argomento |
| `story check` | nessun flag: il file dello storyboard è l'unico argomento |
| `story run` | `--scene <id>` rifà una sola scena; stato e spesa in `<nome>.stato.json` accanto allo storyboard: `tetto_costo_usd` conta la spesa registrata lì (togliendo il file il conto riparte da zero) |

Flag di `giv video` (i valori ammessi dipendono dal modello: vedi
[Video: capacità, costi e preset](#video-capacità-costi-e-preset)):

| Flag | Cosa fa |
|---|---|
| `--aspect <16:9\|9:16>` | aspect ratio; con `--image` xai lo ignora (avviso) |
| `--resolution <720p\|1080p>` | risoluzione |
| `--duration <s>` | durata in secondi; senza, il default del modello |
| `--negative "<t>"` | negative prompt; su openrouter inviato dove il modello lo prevede, altrimenti avviso |
| `--image <file\|url>` | primo fotogramma (image-to-video) |
| `--last-frame <file\|url>` | ultimo fotogramma (xai 1.5 e 1.5-lite, openrouter dove il modello lo prevede) |
| `--ref <file\|url>` | immagine di riferimento, ripetibile (xai `reference_images`, openrouter `input_references`) |
| `--audio on\|off` | audio nativo; senza flag il default del modello |
| `--seed <n>` | seed (openrouter dove il modello lo rispetta; xai lo ignora con avviso) |
| `--param k=v` | parametro passante del modello, ripetibile (openrouter; valore JSON o stringa): solo i parametri che il modello elenca |
| `--timeout <durata>` | tetto di attesa del job (default `15m`) |
| `--preset <nome>` | preset di camera o effetto, ripetibile: al massimo uno di camera e uno di effetto; la frase si aggiunge al prompt |
| `--tutela <t>` | clausola aggiunta coi preset: `prodotto` (default con `--image`), `illustrazione`, `no` |
| `--dry-run` | nessuna generazione: stampa in JSON parametri, avvisi e costo stimato |
| `--max-cost <usd>` | rifiuta (codice 2) se il costo stimato supera il tetto o non è stimabile |
| `--strict` | anche i controlli di rifinitura non supportati (`--negative`, `--seed`, modello fuori catalogo…) sono errori |

## Provider

Tra parentesi il modello usato senza `--model` (la stessa tabella è in `giv --help`).

| `--provider` | Immagini | Video | `--input` | `--aspect` | `--seed` |
|---|---|---|---|---|---|
| `gemini` (provider di default) | ✅ Nano Banana (`gemini-3.1-flash-image`) | ❌ nessuna API video (Veo spento): usa `openrouter` o `xai` | ✅ | ✅ | ✅ |
| `openai` | ✅ gpt-image-* (`gpt-image-2.6-flare`) | ❌ nessuna API video: usa `openrouter` o `xai` | ✅ via edits | ✅ via size | ❌ |
| `xai` | ✅ grok-imagine-image-* (`grok-imagine-image-2.0`) | ✅ grok-imagine-video (`grok-imagine-video-1.5`, 1-15 s, primo e ultimo fotogramma, fino a 14 riferimenti) | ❌ immagini / ✅ video | ❌ immagini / ✅ video | ❌ |
| `openrouter` | ✅ Google, OpenAI, ByteDance, Flux, Recraft… (`google/gemini-3.1-flash-image`) | ✅ Gemini Omni, Seedance, Veo, Wan… (`google/gemini-omni-1.2-flash`); capacità per modello da `/videos/models` | ✅ | ✅ | ✅ |

Note:
- OpenRouter usa gli endpoint media dedicati (`/images`, `/videos` asincrono) — l'unica
  via per i modelli ByteDance; riporta `cost_usd` nel manifest. `giv models --provider
  openrouter` elenca l'intero catalogo immagini e video.
- Video gemini: non supportato (Veo 3.1 spento sulla Gemini API dal 22/10/2026, il
  percorso Veo è rimosso); `giv video --provider gemini` esce subito con codice 2, anche
  con `--model`, senza chiamate, e indica `--provider openrouter --model
  google/gemini-omni-1.2-flash`. Veo resta su OpenRouter (`google/veo-3.1`, `-fast`, `-lite`).
- Video openai: non supportato (Sora spento da OpenAI il 24/09/2026, rimosso nella 0.7.0); `giv video --provider openai`
  esce subito con codice 2, senza chiamate.
- `-n` maggiore di 1 su OpenRouter: l'API `/images` accetta una sola immagine per
  richiesta, quindi `giv` ne fa una per immagine e somma i costi in `cost_usd`. Se una
  richiesta fallisce, le immagini già generate non vengono salvate.
- I nomi modello cambiano spesso: in caso di 404 (codice 3) rifare
  `giv models --provider <p>`.

## Video: capacità, costi e preset

### Prima di spendere

`giv video` controlla la richiesta sulle capacità del modello prima di qualsiasi
download o generazione:

- **da dove**: su openrouter `/videos/models`, letto a ogni invocazione (gratuito:
  fotogrammi, durate, risoluzioni, aspect, audio, seed, parametri passanti, listino); su
  xai una tabella interna (`grok-imagine-video-1.5`, `grok-imagine-video-1.5-lite`,
  `grok-imagine-video`; fonte docs.x.ai al 20/11/2026).
- **controlli strutturali** (cambiano cosa viene generato): `--image`, `--last-frame`,
  `--ref` (anche il numero massimo e, coi riferimenti, risoluzioni e durata ridotte),
  `--duration`, `--resolution`, `--aspect`, `--audio on`, `--param` fuori lista. Se il
  modello non li supporta: **codice 2**, nessuna chiamata, e l'elenco dei modelli dello
  stesso provider che li supportano.
- **controlli di rifinitura**: `--seed` o `--negative` non supportati, modello fuori
  catalogo, riferimenti o audio che il catalogo non dichiara. Avviso su stderr e la
  generazione parte (il controllo ignorato non viene inviato); con `--strict`, codice 2.
- **stima del costo**: prezzo al secondo del listino (su openrouter la tariffa per
  risoluzione e audio) per la durata richiesta, o per quella di default del modello. Per i
  modelli a token (es. Seedance) la stima non c'è.

```bash
giv video --provider openrouter --model alibaba/wan-3.0 --duration 5 --resolution 720p \
  --audio off --image foto.jpg --dry-run "il flacone su un piano di marmo"   # cost_est_usd 0.5, nessuna generazione
giv video … --max-cost 0.50 "…"                                 # stima oltre il tetto: codice 2
giv models --provider openrouter --kind video --caps [--json]    # capacità e $/s di ogni modello video
```

`--dry-run` stampa su stdout `{dry_run, provider, model, prompt, prompt_final, params,
inputs, cost_est_usd, cost_source, warnings}`; serve comunque la chiave del provider.

### Preset di camera ed effetti

- `giv presets` elenca i preset: di camera `static`, `dolly-in`, `dolly-out`, `orbit`,
  `arc-left`, `arc-right`, `crane-up`, `crane-down`, `pan`, `tilt-up`, `push-in-macro`,
  `crash-zoom`, `handheld`, `fpv-drone`, `rack-focus`; di effetto `slow-motion`,
  `timelapse`, `bullet-time`.
- `--preset` si ripete al massimo una volta per tipo (uno di camera, uno di effetto); le
  coppie incompatibili (es. `dolly-in` + `dolly-out`, `slow-motion` + `timelapse`) escono
  con codice 2.
- La frase cambia con la famiglia del modello, ricavata dal nome (`kling`, `veo`,
  `seedance`, `wan`); per le altre (es. `grok`, `omni`) vale la frase generica, con avviso
  (con `--strict` di `presets show`, codice 2).
- Dove la famiglia ha il negative (kling, veo, wan) i negative dei preset si sommano a
  `--negative`; altrove si omettono. Se il modello non accetta il negative (es.
  `alibaba/wan-3.0`, nessun parametro passante) resta un avviso.
- `--tutela` aggiunge una clausola: `prodotto` (default con `--image`: prodotto, etichetta
  e proporzioni come nel primo fotogramma), `illustrazione` (stile piatto, niente testo),
  `no`.
- `giv presets show orbit --model alibaba/wan-3.0 --prompt "…" [--negative "…"]`
  mostra prompt e negative finali, senza chiamate.
- `./giv-presets.json` e `~/.config/giv/presets.json` aggiungono o sostituiscono preset
  (vince il primo), nello stesso formato della libreria incorporata
  (`internal/presets/presets.json`); un file malformato esce con codice 2.

## Recupero dei job video

Un video è un job asincrono: `giv` lo avvia, ne controlla lo stato ogni 10 s fino a
`--timeout` e scarica il file. Se l'attesa si interrompe dopo l'avvio (timeout, rete o
errore del provider: codice 5 con l'id del job nel messaggio; oppure `giv` chiuso a mano),
il job resta sul provider e può essere già addebitato. Invece di rilanciare `giv video`:

```bash
giv job fetch --provider openrouter <id>                  # --out e --name come giv video
giv job fetch --provider xai --timeout 30m --name s2 <id>
```

- **stato su file**: ogni job avviato da `giv video` lascia una riga in `giv-jobs.jsonl`
  nella directory corrente (id, indirizzo di attesa, modello, prompt inviato), creato con
  permessi 0600. `giv job fetch` va lanciato dalla stessa directory: su OpenRouter
  l'indirizzo di attesa arriva solo nella risposta dell'avvio.
- **nessun nuovo avvio**: `job fetch` non manda mai la richiesta di generazione. Controlla
  subito lo stato, aspetta se il job è ancora in corso e scarica il video; manifest e riga
  di `giv-log.jsonl` come `giv video` (`command` `job fetch`, `job_id`, `cost_usd` su
  openrouter).
- **job fallito**: errore con l'id del job, codice 5.
- **rifiuti con codice 2, senza richieste**: provider senza job video (gemini, openai), id
  assente da `giv-jobs.jsonl`, file di stato che non è un file regolare (anche un
  symlink), indirizzo di attesa fuori dall'API del provider (schema, host e porta) o
  diverso dal percorso del job (`<api>/videos/…/<id>`, senza query).
- **la chiave va solo all'API del provider**: nei redirect e negli scaricamenti, fuori da
  schema, host e porta dell'API l'intestazione `Authorization` viene tolta.
- `--timeout` (di `giv video` e di `job fetch`) copre l'attesa, i controlli dello stato e
  lo scaricamento.

## Storyboard: video a più scene

Uno storyboard è un file JSON con le scene di un video da montare. `giv story check` lo
valida e ne stima il costo **senza generare niente**: legge solo i file locali e il
catalogo video del provider (gratuito, ma serve la chiave). L'esempio in `examples/spot/`
passa il controllo:

```bash
giv story check examples/spot/storyboard.json     # JSON su stdout: scene, stime, totale
```

```json
{
  "titolo": "Spot di prova: crema alle erbe, 18 secondi",
  "formato": { "aspect": "16:9", "risoluzione": "720p", "fps": 24 },
  "default": { "provider": "openrouter", "modello": "alibaba/wan-3.0", "audio": "off" },
  "tetto_costo_usd": 3,
  "riferimenti": { "prodotto": ["img/prodotto.png"] },
  "scene": [
    { "id": "s1", "tipo": "video", "durata": 5, "immagine": "img/prodotto.png",
      "preset": ["dolly-in"], "prompt": "il vasetto di crema su un piano di legno chiaro…",
      "voce": "voce/s1.wav" },
    { "id": "s2", "tipo": "video", "durata": 5, "continua_da": "s1", "preset": ["orbit"],
      "prompt": "la camera gira attorno al vasetto…", "voce": "voce/s2.wav" },
    { "id": "s3", "tipo": "video", "durata": 5, "usa": ["prodotto"], "preset": ["static"],
      "prompt": "una mano apre il vasetto e mostra la crema…" },
    { "id": "chiusura", "tipo": "immagine", "durata": 3, "file": "img/logo.png",
      "voce": "voce/chiusura.wav" }
  ],
  "montaggio": { "transizione": "dissolvenza", "uscita": "spot.mp4" }
}
```

Stima dell'esempio: 3 scene da 5 s a 720p su `alibaba/wan-3.0` (0,10 $/s), `cost_total_usd`
1,5. I file in `examples/spot/img/` e `voce/` sono segnaposto (tinte piatte e silenzio):
sostituirli con la foto del prodotto, il logo e la voce vera.

| Campo | Cosa contiene |
|---|---|
| `titolo` | facoltativo |
| `formato` | `aspect`, `risoluzione` e `fps` del video: valgono per le scene video che non li indicano |
| `default` | `provider`, `modello`, `risoluzione`, `aspect`, `audio` (`on`/`off`), `seed`, `tutela`, `param` (oggetto, come `--param`) per tutte le scene video; senza `modello` vale il default di `giv video` per quel provider |
| `tetto_costo_usd` | tetto in dollari: un totale oltre il tetto, o una generazione senza stima né costo dichiarato, esce con codice 2 |
| `strict` | come `--strict` di `giv video`: anche i controlli di rifinitura sono errori |
| `riferimenti` | insiemi di immagini per nome (`"prodotto": ["img/prodotto.png"]`), richiamati dalle scene con `usa` |
| `scene` | le scene, in ordine |
| `montaggio` | `transizione` (`taglio`, default, o `dissolvenza`), `musica` (`file`, `volume` da 0 a 1), `sottotitoli` (file), `uscita` (file finale) |

Ogni scena ha un `id` (lettere, cifre, `_` e `-`, al massimo 64 caratteri), un `tipo`, una
`durata` in secondi e, facoltativa, la `voce`: un file audio fuori campo, messo al
montaggio.

- `video`: generata da un modello. `prompt`, `negative`, `preset`; il primo fotogramma
  viene da **uno solo** fra `immagine` (file o URL), `fotogramma_chiave` (immagine
  generata prima con `giv image`: `prompt`, `provider`, `modello`, `costo_dichiarato_usd`;
  gli insiemi di `usa` ne sono gli ingressi) e `continua_da` (id di una scena video o clip
  precedente, di cui si usa l'ultimo fotogramma); poi `ultimo_fotogramma`, `usa`,
  `costo_dichiarato_usd` e i campi di `default`, che la scena sovrascrive. Senza `durata`
  vale il default del modello.
- `clip`: un `file` video già pronto (ripresa vera, screencast).
- `immagine`: un `file` immagine fermo, con `durata` obbligatoria (cartelli, chiusure).
- `avatar` non c'è in giv 0.9.0 e viene rifiutato: la voce va nel campo `voce` di una
  scena.

`giv story check` esce con codice 2 e l'elenco di tutti i problemi trovati, uno per riga:

- **forma**: JSON con campi sconosciuti, id mancanti o ripetuti, tipi sconosciuti, campi
  che non valgono per il tipo di scena, `continua_da` verso una scena successiva o
  inesistente, più di un primo fotogramma;
- **file**: relativi alla cartella dello storyboard, esistenti, regolari e dentro la
  cartella anche seguendo i link; un URL solo per `immagine`, `ultimo_fotogramma` e
  `riferimenti`;
- **scene video**: ognuna come `giv video --dry-run`, sul catalogo letto in quel momento
  (un modello che non c'è più viene rifiutato), con capacità, preset e stima; gli avvisi
  sono nei `warnings` della scena;
- **costi**: `cost_est_usd` (stime da listino), `cost_declared_usd` (costi dichiarati
  dall'autore dove la stima non c'è, per esempio i modelli a token come Seedance),
  `cost_total_usd`; le generazioni senza stima né costo dichiarato (compresi i
  fotogrammi chiave) sono in `not_estimated` e non contano come zero. Con
  `tetto_costo_usd` vengono rifiutate.

## Uso da un agente (Claude Code)

La skill operativa è nel repo e si installa con un symlink:

```bash
ln -s "$(pwd)/skills/gen-image-video" ~/.claude/skills/gen-image-video
```

Da quel momento qualsiasi sessione sa usare il CLI: contratto di output, matrice
provider, ricette (reference di stile, image-to-video, spot da foto di prodotto, mascotte
fra scene, preset di camera, spot a più scene con lo storyboard, voce fuori campo),
controlli prima di spendere, recupero dei job, costi indicativi e quirk.

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
internal/config/          risoluzione credenziali (env → credentials.env → .env → ~/.config/giv/credentials.env)
internal/httpx/           errori HTTP tipizzati, retry (5xx, polling video) e job video non ripetuto
internal/provider/        interfaccia Provider + tipi comuni
internal/provider/gemini/ adapter Gemini (generateContent; niente video)
internal/provider/openaicompat/  adapter OpenAI-compatible (openai, xai, openrouter)
internal/caps/            capacità e prezzi dei modelli video (OpenRouter /videos/models, tabella xAI), controlli e stima
internal/presets/         libreria dei preset (presets.json incorporato) e composizione del prompt
internal/jobs/            stato dei job video avviati (giv-jobs.jsonl), per giv job fetch
internal/story/           formato e controlli dello storyboard di giv story
internal/output/          salvataggio media + manifest JSON
skills/gen-image-video/   skill agent-native
examples/spot/            storyboard d'esempio con file segnaposto
```

## Versioning

SemVer, storia in [CHANGELOG.md](CHANGELOG.md). La versione del binario è in
`cmd/giv/main.go` (`const version`) e va aggiornata insieme al changelog a ogni release.
