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
  | 5 | errore dell'API o di rete | riprovare; per i video leggere l'id del job nel messaggio |
  | 7 | troppe richieste (HTTP 429) | aspettare e riprovare |
  | 10 | chiave API mancante | impostare la chiave del provider |
- **manifest completo** — path dei file scritti, **mime reale** restituito dal provider
  (non dedotto dall'estensione), byte, e `cost_usd` quando il provider riporta il costo
  (OpenRouter, immagini e video): un agente con un budget sa quanto ha speso.
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
  corrente; `giv log` la storia, `giv log --sum-cost` il totale speso registrato.
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
giv log     [-n N] [--sum-cost] [--json]      # registro locale delle generazioni
giv presets [--json]                          # preset di camera ed effetti
giv presets show <nome> [<nome>] [--model M] [--prompt "<t>"] [--tutela prodotto|illustrazione]
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
| `openrouter` | ✅ Google, OpenAI, ByteDance, Flux, Recraft… (`google/gemini-3.1-flash-image`) | ✅ Gemini Omni, Seedance, Veo, Kling, Wan… (`google/gemini-omni-1.2-flash`); capacità per modello da `/videos/models` | ✅ | ✅ | ✅ |

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
giv video --provider openrouter --model kwaivgi/kling-v3.0-pro --duration 5 --audio off \
  --image foto.jpg --dry-run "il flacone su un piano di marmo"   # cost_est_usd 0.56, nessuna generazione
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
  `seedance`, `wan`); per le altre (es. `grok`, `omni`) vale la frase generica, con avviso.
- `--tutela` aggiunge una clausola: `prodotto` (default con `--image`: prodotto, etichetta
  e proporzioni come nel primo fotogramma), `illustrazione` (stile piatto, niente testo),
  `no`.
- `giv presets show orbit --model kwaivgi/kling-v3.0-pro --prompt "…"` mostra la frase
  finale, senza chiamate.
- `./giv-presets.json` e `~/.config/giv/presets.json` aggiungono o sostituiscono preset
  (vince il primo), nello stesso formato della libreria incorporata
  (`internal/presets/presets.json`); un file malformato esce con codice 2.

## Uso da un agente (Claude Code)

La skill operativa è nel repo e si installa con un symlink:

```bash
ln -s "$(pwd)/skills/gen-image-video" ~/.claude/skills/gen-image-video
```

Da quel momento qualsiasi sessione sa usare il CLI: contratto di output, matrice
provider, ricette (reference di stile, image-to-video, spot da foto di prodotto, mascotte
fra scene, preset di camera), controlli prima di spendere, costi indicativi e quirk.

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
internal/output/          salvataggio media + manifest JSON
skills/gen-image-video/   skill agent-native
```

## Versioning

SemVer, storia in [CHANGELOG.md](CHANGELOG.md). La versione del binario è in
`cmd/giv/main.go` (`const version`) e va aggiornata insieme al changelog a ogni release.
