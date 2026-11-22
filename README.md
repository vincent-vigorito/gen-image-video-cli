# gen-image-video-cli

**CLI agent-native per generare immagini e video** con i principali provider AI —
Google Gemini, OpenAI, x.ai, OpenRouter (che aggiunge i motori ByteDance
Seedream/Seedance) e fal (Kling e Seedance). Un solo binario, `giv`, progettato per
essere **usato dagli agenti** — Claude Code, LLM con tool-use, script e pipeline CI — prima ancora che
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
  quando il provider non riporta il costo, es. xai; nei comandi sulle clip restano accanto
  a `cost_usd`), `warnings[]`.
- **naming deterministico** — `<out>/<slug>-<timestamp>[-n].<ext>`; `--name` per slug
  stabili, `--out` per la directory. Un file che c'è già non si sovrascrive mai (né si
  segue un link al suo posto): il nuovo prende il suffisso `-2`, `-3`…
- **credenziali senza interazione** — env di processo → `./credentials.env` → `./.env` →
  `~/.config/giv/credentials.env` (così `giv` funziona da qualsiasi directory); niente
  login, niente browser. Lo stesso vale per `GIV_PROVIDER`, il provider usato senza
  `--provider` (default `gemini`, solo per le immagini: un video, a pagamento, parte solo
  con un provider scelto, vedi [Provider](#provider)).
- **retry automatico sui 5xx transitori** — fino a 2 ritentativi con backoff (avviso su
  stderr): un blip del provider non fa fallire il task dell'agente. Non si ripetono le
  richieste a pagamento (avvio di un job, immagini su OpenRouter) né i caricamenti di file.
- **attesa dei video che riprova** — durante il polling del job, 5xx, 429 ed errori di
  rete sono ritentati (fino a 5 volte, con `Retry-After` quando c'è): un disturbo
  passeggero non butta un video già avviato. Se l'errore resta, il messaggio riporta
  l'**id del job** per ritrovarlo sul provider.
- **creazione del job video non ripetuta** — la richiesta che avvia il video non viene
  mai rilanciata da sola dopo un 5xx o un timeout e non segue redirect (il job potrebbe
  essere partito e un secondo invio costarebbe due video): `giv` esce con codice 5 e un
  suggerimento. Vale anche per `giv extend`, `edit`, `upscale` e `avatar` e per `giv
  image` su OpenRouter.
- **recupero dei job** — ogni job video avviato lascia il suo stato in `giv-jobs.jsonl`;
  `giv job fetch --provider P <id>` ne riprende l'attesa e scarica il video **senza un
  nuovo avvio** (vedi [Recupero dei job video](#recupero-dei-job-video)).
- **operazioni su una clip** — `giv extend` e `giv edit` allungano o modificano una clip
  (xAI, oppure Gemini sui video che ha generato), `giv upscale` la ingrandisce (OpenRouter
  o fal), `giv avatar` fa parlare un personaggio da un'immagine e una voce (fal): stessi controlli prima di spendere, stima, `--dry-run`,
  `--max-cost`, manifest e recupero dei job di `giv video` (vedi
  [Operazioni su una clip](#operazioni-su-una-clip)).
- **scaricamenti protetti** — i risultati indicati dal provider fuori dalla sua API si
  scaricano solo da `https` verso indirizzi pubblici, senza chiave né credenziali, coi
  redirect ricontrollati e senza `Referer`. I messaggi d'errore del provider arrivano
  filtrati: niente corpo grezzo, URL ridotti a schema e host.
- **storyboard a più scene** — `giv story check` valida un file JSON con le scene di uno
  spot (modello, file, preset, riferimenti fra scene) e ne stima il costo senza generare
  niente; `giv story run` genera le scene video riprendendo da dove si era fermato, senza
  rilanciare i job già partiti e dentro un tetto di spesa cumulativo; `giv story cut`
  monta con ffmpeg le scene in un solo video, con voce, musica e sottotitoli (vedi
  [Storyboard](#storyboard-video-a-più-scene)).
- **capacità e costo prima di spendere** — prima di avviare un video `giv` controlla il
  modello (OpenRouter: `/videos/models`; xAI e Gemini: tabella interna; fal: schede di giv). Un controllo che il
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
  (`cost_est_usd`, delle sole righe senza costo riportato), più i job falliti (`failed_jobs`, `failed_cost_est_usd`); con
  `--by provider|model` anche i totali per gruppo.
- **input da URL** — `--input`, `--image`, `--last-frame`, `--ref` e `--keyframe` accettano
  anche URL http(s), scaricati da soli.
- **prezzi interrogabili** — `giv models --provider openrouter --json` include il listino
  per modello; `giv models --kind video --caps` dà capacità e prezzo al secondo dei
  modelli video (gemini, xai, openrouter, fal): un agente può scegliere il motore economico senza
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
giv extend  --video <mp4|url> [--duration 2-10] "<come continua>"
                                              # allunga una clip (xai, job asincrono, a pagamento)
giv extend  --provider gemini --from <job_id> [--duration 3-10] "<come continua>"
                                              # allunga un video fatto con gemini (a pagamento)
giv edit    --video <mp4|url> "<cosa cambiare>"  # modifica una clip (xai, job asincrono, a pagamento)
giv edit    --provider gemini --from <job_id> "<cosa cambiare>"
                                              # modifica un video fatto con gemini (a pagamento)
giv job fetch --provider P [--out D] [--name S] [--timeout 15m] <id>
                                              # scarica un job video già partito, senza nuovo avvio
giv story check <storyboard.json>             # valida uno storyboard a più scene e ne stima il costo
giv story run [--scene <id> [--new-keyframe]] <storyboard.json>
                                              # genera le scene video (a pagamento), riprende da dove si era fermato
giv story cut [--formato <r>] [--export <canali>] <storyboard.json>
                                              # monta con ffmpeg le scene già pronte, un file per formato
giv story help                                # aiuto di giv story; giv story <sottocomando> --help per il sottocomando
giv kit check <kit.json>                      # controlla il kit di marca e personaggi ed elenca i file con le misure (gratuito)
giv log     [-n N] [--sum-cost [--by provider|model]] [--json]   # registro locale delle generazioni
giv presets [--json]                          # preset di camera ed effetti
giv presets show <nome> [<nome>] [--model M] [--prompt "<t>"] [--negative "<t>"]
                 [--tutela prodotto|illustrazione] [--strict]
                                              # frase finale dei preset per quel modello, senza chiamate
giv upscale --video <url|file> [--provider openrouter|fal] [--factor 1.5-3|fal 1-4] [--mode precise|creative] ["<prompt>"]
                                              # ingrandisce una clip (file locale solo con fal; openrouter vuole un URL https; a pagamento)
giv avatar  --image <file|url> --audio <wav|mp3> [--model standard|pro] ["<prompt>"]
                                              # personaggio che parla (fal, job asincrono, a pagamento)
giv post <operazione> --video <file> [flag dell'operazione] [--out D] [--name S] [--dry-run]
                                              # rifinitura in locale con ffmpeg, gratuita: trim, speed, freeze,
                                              # reframe, overlay, mix (giv post help)
giv sheet   <file|cartella|storyboard.json> [--every <s> | --frames <n>] [--cols <n>] [--labels]
                                              # foglio di contatto JPEG (locale con ffmpeg, gratuito)
giv frame   --video <file> --at <s> | --last [--width <px>]
                                              # un fotogramma PNG, con --width la miniatura (locale, gratuito)
giv gif     --video <file> [--start <s>] [--end <s>] [--width <px>] [--fps <n>]
                                              # GIF animata, palette in due passate (locale, gratuita)
giv export  --video <file> --for instagram,tiktok,youtube,linkedin [--fit crop|pad|blur] [--focus <x>[,<y>]] [--loudness <lufs>|off]
                                              # un file per canale col suo profilo, in locale con ffmpeg (gratuito)
giv voice   "<testo>" | --text-file <f> [--provider xai|gemini] [--voice <nome>] [--lang it] [--speed 0.7-1.5] [--kit <kit.json>] [--pronuncia <file.json>]
                                              # voce fuori campo WAV, coi tempi per carattere su xai (a pagamento; giv voice help)
giv voice   --voices [--provider xai|gemini]  # elenco delle voci del provider (gratuito)
giv version
```

Flag comuni: `--provider` (gemini | openai | xai | openrouter | fal; senza flag vale
`GIV_PROVIDER`, altrimenti gemini; per `giv video` senza `--provider` né `GIV_PROVIDER`
codice 2; per extend, edit, upscale e avatar il provider dell'operazione), `--model` (default per provider, vedi sotto),
`--out`, `--name`. Il prompt è un **singolo argomento quotato**; i flag possono stare
prima o dopo.

| Comando | Flag specifici |
|---|---|
| `image` | `-n <num>` (su OpenRouter una richiesta per immagine, costi sommati) · `--aspect <1:1\|16:9\|9:16\|4:3\|3:4>` · `--seed <n>` · `--input <file\|url>` (ripetibile: reference di stile / editing) |
| `video` | tabella sotto |
| `extend`, `edit` | provider `xai` (default): `--video <mp4\|url>` obbligatorio; provider `gemini`: `--from <job_id>` obbligatorio (un video fatto con `giv video --provider gemini`, niente `--video`) · `--duration` (solo `extend`: secondi aggiunti, xai 2-10 e default 6, gemini 3-10 e default 10) · `--dry-run` · `--max-cost <usd>` · `--strict` · `--timeout` |
| `upscale` | `--video <url\|file>` obbligatorio (con openrouter solo URL `https` pubblico: un file locale esce con codice 2; con fal anche file locale) · `--factor` (default 2; openrouter 1,5-3, fal 1-4) · `--mode precise\|creative` (default `precise`) e prompt facoltativo, solo openrouter · `--dry-run` · `--max-cost` · `--strict` · `--timeout`; provider `openrouter` (default) o `fal` |
| `avatar` | `--image <file\|url>` e `--audio <wav\|mp3>` obbligatori · `--model standard\|pro` (default `standard`) · prompt facoltativo · `--dry-run` · `--max-cost` · `--strict` · `--timeout`; provider `fal` |
| `job fetch` | `--provider` (gemini, xai, openrouter o fal, quello del job) · `--out` · `--name` · `--timeout <durata>` (default `15m`); l'id del job è l'unico argomento |
| `story check` | nessun flag: il file dello storyboard è l'unico argomento |
| `story run` | `--scene <id>` rifà una sola scena; `--new-keyframe` (solo con `--scene`) rigenera anche il suo fotogramma chiave; `--new-job` (solo con `--scene`) abbandona il job già partito della scena che non si scarica più (addebito contato, job fra i precedenti) e ne avvia uno nuovo; stato e spesa in `<nome>.stato.json` accanto allo storyboard: `tetto_costo_usd` conta la spesa registrata lì (togliendo il file il conto riparte da zero) |
| `story cut` | `--formato <r>` monta solo quel formato (16:9, 9:16, 1:1 o 4:5; fra `montaggio.formati`, se ci sono) in `<nome>-<WxH>.mp4`; senza, i formati di `montaggio.formati` o, se mancano, un solo file come prima; `--export <canali>` (come `giv export --for`) esporta per canale il file montato nel formato del canale, in `<nome>-<canale>-<WxH>.mp4`, al volume di `montaggio.volume` o a -14 LUFS |
| `kit check` | nessun flag: il file del kit è l'unico argomento; `giv kit help` (o `giv kit --help`) stampa l'aiuto |
| `post <operazione>` | `--video <file>` obbligatorio · flag dell'operazione · `--out` · `--name` · `--dry-run`: vedi [Rifinitura in locale](#rifinitura-in-locale-giv-post) |
| `sheet` | file, cartella o storyboard come argomento (o `--video`) · `--every <s>` oppure `--frames <n>` · `--cols <n>` (default 4) · `--labels` · `--out` · `--name` · `--dry-run`: vedi [Foglio di contatto, fotogramma e GIF](#foglio-di-contatto-fotogramma-e-gif-giv-sheet-giv-frame-giv-gif) |
| `voice` | testo fra virgolette oppure `--text-file <file>` · `--provider xai\|gemini` (default `xai`) · `--model <m>` (gemini) · `--voice <nome>` · `--lang <codice>` (default `it`) · `--speed <0.7-1.5>` (xai) · `--style "<t>"` (gemini) · `--kit <kit.json>` · `--pronuncia <file.json>` · `--out` · `--name` · `--dry-run` · `--max-cost <usd>` · `--voices`: vedi [Voce fuori campo](#voce-fuori-campo-giv-voice) |
| `frame` | `--video <file>` obbligatorio · `--at <s>` oppure `--last` · `--width <px>` · `--out` · `--name` · `--dry-run` |
| `gif` | `--video <file>` obbligatorio · `--start <s>` · `--end <s>` · `--width <px>` (default 480) · `--fps <n>` (default 12) · `--out` · `--name` · `--dry-run` |
| `export` | `--video <file>` obbligatorio · `--for <canali>` obbligatorio: `instagram`, `tiktok`, `youtube`, `linkedin` separati da virgole, ognuno col formato facoltativo `<canale>:<formato>` (`9:16`, `1:1`, `4:5`, `16:9`; predefiniti: 9:16 per Instagram (Reel) e TikTok, 16:9 per YouTube e LinkedIn; es. `instagram:4:5`) · `--fit crop\|pad\|blur` (default `crop`) · `--focus <x>[,<y>]` (solo con `crop`, da 0 a 1, default centro) · `--loudness <lufs>\|off` (default -14 LUFS, convenzione dei social) · `--out` · `--name` (file `<nome>-<canale>-<formato>.mp4`) · `--dry-run`. Misure 1080x1920, 1080x1080, 1080x1350, 1920x1080; H.264 e AAC 48 kHz, moov in testa; durata fuori dai limiti del canale (Instagram 3 s-15 min, TikTok 10 min, YouTube 15 min e Shorts 3 min, LinkedIn 3 s-10 min) = codice 2 prima di scrivere; gratuito, nessuna chiamata a provider |

Flag di `giv video` (i valori ammessi dipendono dal modello: vedi
[Video: capacità, costi e preset](#video-capacità-costi-e-preset)):

| Flag | Cosa fa |
|---|---|
| `--aspect <16:9\|9:16>` | aspect ratio; con `--image` xai lo ignora (avviso) |
| `--resolution <720p\|1080p>` | risoluzione |
| `--duration <s>` | durata in secondi; senza, il default del modello |
| `--negative "<t>"` | negative prompt; su openrouter inviato dove il modello lo prevede, altrimenti avviso |
| `--image <file\|url>` | primo fotogramma (image-to-video) |
| `--last-frame <file\|url>` | ultimo fotogramma (xai 1.5 e 1.5-lite, gemini dopo `--image`, openrouter dove il modello lo prevede) |
| `--ref <file\|url>` | immagine di riferimento, ripetibile (xai `reference_images`, openrouter `input_references`, gemini fino a 6) |
| `--keyframe <file\|url>` | fotogramma intermedio, ripetibile e in ordine (fal kling-4.0, al massimo 8) |
| `--shot "<s>:<testo>"` | inquadratura di s secondi interi, ripetibile e in ordine (fal kling-4.0 e kling-v3-pro): niente prompt né `--preset`, la durata è la somma (un `--duration` diverso esce con codice 2) |
| `--audio on\|off` | audio nativo; senza flag il default del modello |
| `--seed <n>` | seed (openrouter dove il modello lo rispetta; xai e gemini lo ignorano con avviso) |
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
| `gemini` (provider di default delle immagini) | ✅ Nano Banana (`gemini-3.1-flash-image`) | ✅ Gemini Omni diretto (`gemini-omni-1.2-flash`, 3-15 s, audio sempre, primo e ultimo fotogramma, fino a 6 riferimenti), solo con il provider scelto; extend ed edit dei suoi video con `--from` | ✅ | ✅ | ✅ immagini / ❌ video |
| `openai` | ✅ gpt-image-* (`gpt-image-2.6-flare`) | ❌ nessuna API video: usa `openrouter` o `xai` | ✅ via edits | ✅ via size | ❌ |
| `xai` | ✅ grok-imagine-image-* (`grok-imagine-image-2.0`) | ✅ grok-imagine-video (`grok-imagine-video-1.5`, 1-15 s, primo e ultimo fotogramma, fino a 14 riferimenti) | ❌ immagini / ✅ video | ❌ immagini / ✅ video | ❌ |
| `openrouter` | ✅ Google, OpenAI, ByteDance, Flux, Recraft… (`google/gemini-3.1-flash-image`) | ✅ Gemini Omni, Seedance, Veo, Wan… (`google/gemini-omni-1.2-flash`); capacità per modello da `/videos/models` | ✅ | ✅ | ✅ |
| `fal` | ❌ | ✅ schede fissate in giv: Kling 3 Pro (`kling-v3-pro`, 3-15 s, primo e ultimo fotogramma, 2-4 riferimenti, inquadrature), Kling 4.0 (`kling-4.0`, 3-30 s, fino a 8 fotogrammi intermedi, inquadrature) e Seedance 2.0 da riferimenti (`seedance-2.0-ref`, 4-15 s, 1-9 riferimenti); avatar con `giv avatar`, ingrandimento Topaz con `giv upscale --provider fal` | ❌ | ❌ Kling / ✅ Seedance | ❌ |

Note:
- OpenRouter usa gli endpoint media dedicati (`/images`, `/videos` asincrono) — l'unica
  via per i modelli ByteDance; riporta `cost_usd` nel manifest. `giv models --provider
  openrouter` elenca l'intero catalogo immagini e video.
- Video gemini: `gemini-omni-1.2-flash` sulla Gemini API (interactions), a pagamento.
  Non ancora provato dal vivo: nella 0.10.0 è provato solo contro server finti.
  Parte solo con una scelta esplicita del provider: `--provider gemini`, il provider dello
  storyboard o `GIV_PROVIDER`. Senza nessuno dei tre `giv video` e `giv story` escono con
  codice 2, senza chiamate, e dicono quali provider scegliere (come nella 0.9.1: un
  comando o uno storyboard di prima non comincia a spendere). Audio sempre presente
  (`--audio off` è rifiutato: per guidarlo va scritto nel prompt, es. "No dialogue");
  durata 3-15 s (default 10), risoluzione 360p, 720p (default), 1080p o 4k, aspect 16:9 o
  9:16: durata e risoluzione vanno sempre nella richiesta. `--last-frame` vuole anche
  `--image` e porta la durata al massimo a 10 s; fino a 6 `--ref`, ma non insieme a
  `--image` né a `--last-frame`; `--seed` ignorato con avviso. La chiave va solo
  all'origine dell'API; la richiesta che genera non segue redirect e non si ripete; il
  video arriva nella risposta o si scarica dall'API dei file, fuori dall'API solo da
  `https` verso indirizzi pubblici e senza chiave. Il job si recupera con `giv job fetch
  --provider gemini <id>`. Veo resta su OpenRouter (`google/veo-3.1`, `-fast`, `-lite`):
  sulla Gemini API è spento dal 22/10/2026.
- Video openai: non supportato (Sora spento da OpenAI il 24/09/2026, rimosso nella 0.7.0); `giv video --provider openai`
  esce subito con codice 2, senza chiamate.
- `-n` maggiore di 1 su OpenRouter: l'API `/images` accetta una sola immagine per
  richiesta, quindi `giv` ne fa una per immagine e somma i costi in `cost_usd`. Se una
  richiesta fallisce, le immagini già generate non vengono salvate.
- fal: non ancora provato dal vivo (nella 0.10.0 provato solo contro server finti). Chiave
  `FAL_KEY`. Nessun catalogo letto in rete: `giv models --provider fal` (anche
  `--kind video --caps`) elenca le schede di giv, con capacità, regole e prezzi presi dalle
  pagine dei modelli il 22/11/2026; un `--model` senza scheda esce con codice 2. Il job
  parte sulla coda di fal (`queue.fal.run`) una volta sola; immagini e voci locali vanno
  sullo storage di fal (`rest.fal.ai`, in due passi, con scadenza di un'ora; per il resto
  vale la conservazione dei dati predefinita di fal). La chiave va solo a queste due
  origini: il caricamento firmato e lo scaricamento del video partono senza chiave, solo
  `https` verso indirizzi pubblici.
- Schede fal, codice 2 prima di ogni chiamata: `kling-v3-pro` vuole `--image` e, se ci
  sono, da 2 a 4 `--ref` (la prima frontale); non accetta `--resolution` né `--aspect` (il
  formato segue l'immagine); prompt fino a 2500 caratteri; parametri passanti `cfg_scale`
  e `negative_prompt`; inquadrature (`--shot`) da 1 a 15 s, testo fino a 2500 caratteri.
  `kling-4.0` vuole `--image`; 3-30 s (default 5); risoluzione 720p, 1080p (default,
  sempre inviata) o 4k; non accetta `--aspect`, `--audio` (la scheda non ha un campo per
  l'audio) né `--ref`; fino a 8 `--keyframe` in ordine fra primo e ultimo fotogramma;
  inquadrature da 1 a 30 s, testo fino a 512 caratteri; prompt fino a 26000 caratteri.
  `--keyframe` e `--shot` con un altro provider o modello escono con codice 2.
  `seedance-2.0-ref` vuole da 1 a 9 `--ref` e un prompt; risoluzioni
  480p, 720p (default) e 1080p; aspect `auto`, 21:9, 16:9, 4:3, 1:1, 3:4, 9:16. Su fal
  `--seed` non vale (avviso).
- I nomi modello cambiano spesso: in caso di 404 (codice 3) rifare
  `giv models --provider <p>`.

## Video: capacità, costi e preset

### Prima di spendere

`giv video` controlla la richiesta sulle capacità del modello prima di qualsiasi
download o generazione:

- **da dove**: su openrouter `/videos/models`, letto a ogni invocazione (gratuito:
  fotogrammi, durate, risoluzioni, aspect, audio, seed, parametri passanti, listino); su
  xai una tabella interna (`grok-imagine-video-1.5`, `grok-imagine-video-1.5-lite`,
  `grok-imagine-video`; fonte docs.x.ai al 20/11/2026); su gemini una tabella interna
  (`gemini-omni-1.2-flash`, documentazione e listino di AI Studio al 22/11/2026); su fal le
  schede di giv (pagine dei modelli al 22/11/2026).
- **controlli strutturali** (cambiano cosa viene generato): `--image`, `--last-frame`,
  `--ref` (anche il numero massimo e, coi riferimenti, risoluzioni e durata ridotte),
  `--keyframe`, `--shot`, `--duration`, `--resolution`, `--aspect`, `--audio on`,
  `--param` fuori lista. Se il
  modello non li supporta: **codice 2**, nessuna chiamata, e l'elenco dei modelli dello
  stesso provider che li supportano.
- **combinazioni che il provider rifiuta**: via openrouter i modelli `x-ai/…` rispondono
  400 a `--image` e `--ref` insieme; giv esce prima, con codice 2, e propone uno solo dei
  due oppure `--provider xai`, che li accetta.
- **controlli di rifinitura**: `--seed` o `--negative` non supportati, modello fuori
  catalogo, riferimenti o audio che il catalogo non dichiara. Avviso su stderr e la
  generazione parte (il controllo ignorato non viene inviato); con `--strict`, codice 2.
- **stima del costo**: prezzo al secondo del listino (su openrouter la tariffa per
  risoluzione e audio) per la durata richiesta, o per quella di default del modello. Su
  openrouter per i modelli a token (es. Seedance) la stima non c'è. Su fal Kling 3 Pro
  costa 0,168 $/s con audio e 0,14 senza (il più alto fra le fonti, da confermare) e
  Seedance 2.0 si stima con la formula della scheda: token = larghezza × altezza ×
  secondi × 24 / 1024, a 0,014 $ ogni 1000 token (senza `--aspect` il formato più largo,
  senza `--resolution` 720p). Kling 4.0 costa 0,157 $/s a 720p, 0,235 a 1080p e 0,549 in
  4k (stimato senza audio). Gemini si paga a token: la stima è un massimo, con i secondi
  d'uscita alla risoluzione inviata (0,0695 $/s a 720p, 0,1043 a 1080p, 0,2085 in 4k,
  0,0232 a 360p), più i token d'ingresso (prompt e margine; con immagini il limite del
  modello) e il massimo dell'uscita di testo e ragionamento (65536 token, circa 0,59 $). A
  fine job il manifest affianca alla stima `cost_usage_usd` e `cost_usage_source`, il costo
  ricavato dai token che Gemini riporta; nel conto di `giv story run` prende il posto della
  riserva solo se non è più basso.
- **tetto di spesa**: `--max-cost` esce con codice 2, senza chiamate, se la stima supera
  il tetto o non è possibile. Anche quando, senza `--audio` o `--resolution`, una
  configurazione ammessa dal modello non ha tariffa nel listino (la stima vale solo per
  quelle coperte): il messaggio la nomina e dice quale flag indicare. Per esempio
  `google/gemini-omni-1.2-flash`, il default di openrouter, con `--max-cost` vuole
  `--audio on`. Senza `--max-cost` stima e piano restano quelli di prima.

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
  `seedance`, `grok`, `wan`; per `grok` le varianti valgono per `grok-imagine-video-*` di
  xai); per le altre (es. `omni`) vale la frase generica, con avviso (con `--strict` di
  `presets show`, codice 2).
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

## Operazioni su una clip

`giv extend`, `giv edit`, `giv upscale` e `giv avatar` partono da una clip o da
un'immagine che c'è già, con lo stesso giro di `giv video`:

- **controlli, codice 2 prima di ogni chiamata**: modello e flag sull'operazione (un flag
  obbligatorio vuoto o a zero vale mancante, un flag che l'operazione non usa è rifiutato),
  file locali regolari e riconosciuti dai byte, URL con schema e host e senza credenziali;
- **provider**: senza `--provider` quello dell'operazione (xai per extend ed edit,
  openrouter per upscale, fal per avatar), non `GIV_PROVIDER`; con `--provider` anche
  gemini per extend ed edit e fal per upscale;
- **misure**: durata e lati dei file si leggono con ffprobe; senza misura (ffprobe assente
  o sorgente da URL) i limiti non si controllano (avviso) e la stima non c'è, quindi
  `--max-cost` esce con codice 2;
- **uscita**: `--dry-run` stampa piano, avvisi e stima senza chiamate; il manifest ha
  `command` (`extend`, `edit`, `upscale`, `avatar`) e `inputs` (percorso e impronta dei
  file, solo schema e host degli URL); `cost_est_usd` e `cost_source` restano accanto al
  `cost_usd` riportato dal provider; il job resta in `giv-jobs.jsonl` e si recupera con
  `giv job fetch` come un video.

### Allungare e modificare (xAI)

```bash
giv extend --video clip.mp4 --duration 6 "la camera si allontana e mostra la città"
giv edit --video https://example.com/clip.mp4 "aggiungi la pioggia"
giv extend --video clip.mp4 --dry-run --max-cost 1.00 "…"       # stima, nessuna chiamata
```

- **sorgente**: `--video` è un file mp4 locale (riconosciuto dai byte, al massimo 50 MiB),
  caricato su xAI (`/v1/files`, scade dopo un'ora), oppure un URL `https` che finisce in
  `.mp4`, passato così com'è (giv non lo scarica). Solo `grok-imagine-video`.
- **limiti, codice 2 prima di ogni chiamata**: prompt obbligatorio; `extend` aggiunge da 2
  a 10 s (default 6) a un sorgente di 2-15 s; `edit` vuole un sorgente fino a 8,7 s e non
  ha `--duration`; non ammessi `--image`, `--audio`, `--resolution`, `--aspect`,
  `--seed`, `--negative`, `--ref`. Sorgente oltre 720p: avviso (l'uscita resta a 720p),
  errore con `--strict`. Durata e altezza si leggono con ffprobe: senza ffprobe, o da URL,
  i limiti non si controllano (avviso).
- **costo**: `extend` = secondi aggiunti × 0,07 $/s + sorgente (a secondi interi per
  eccesso) × 0,01 $/s, `edit` = sorgente × 0,08 $/s; l'uscita si conta a 720p, la tariffa
  più alta. È la fatturazione vista nella prova del 22/11/2026: 3 s aggiunti a una clip di
  6 s, 0,27 $ (3 × 0,07 + 6 × 0,01); la modifica della stessa clip 0,48 $. Senza la durata
  misurata del sorgente (URL o ffprobe assente) la stima non c'è e `--max-cost` rifiuta
  con codice 2. A fine job il manifest riporta il costo reale di xAI (`cost_usd`), con la
  stima accanto.
- manifest con `command` `extend`/`edit`, `inputs.video` (percorso e impronta, o solo
  schema e host per un URL); il job resta in `giv-jobs.jsonl` e si recupera con
  `giv job fetch --provider xai <id>`.

### Allungare e modificare con Gemini (`--from`)

```bash
giv video --provider gemini --duration 8 "un faro sulla scogliera al tramonto"   # job_id nel manifest
giv extend --provider gemini --from <job_id> --duration 6 --dry-run "la camera sale sopra il faro"
giv edit --provider gemini --from <job_id> "aggiungi la nebbia"
```

- **sorgente**: dall'UE Gemini estende e modifica solo i video che ha generato, non file
  caricati: `--from` è l'id del job di un `giv video --provider gemini` (o di un extend o
  edit gemini), `--video` è rifiutato con codice 2. Prompt obbligatorio.
- **limiti**: `extend` aggiunge da 3 a 10 s (default 10) fino a 30 s in tutto; `edit` vale
  per un video fino a 15 s e non ha `--duration`.
- **misure**: durata e risoluzione del video di partenza vengono da `giv-log.jsonl` della
  directory corrente (risalendo la catena dei `--from`); se il job non c'è, avviso, i
  limiti non si controllano e la stima non c'è, quindi `--max-cost` esce con codice 2.
- **costo**: stimato come `giv video` gemini sui secondi d'uscita alla risoluzione del
  video di partenza (per `extend` partenza più aggiunta, finché non è verificato che si
  paghino solo i secondi aggiunti), con l'ingresso al limite del modello; a fine job
  `cost_usage_usd` dai token riportati. Recupero con `giv job fetch --provider gemini <id>`.

### Ingrandire (`giv upscale`)

```bash
giv upscale --video https://example.com/clip.mp4 --factor 2 --mode precise
giv upscale --video https://example.com/clip.mp4 --mode creative "più nitido, stessa luce"
giv upscale --provider fal --video clip.mp4 --factor 2 --dry-run   # Topaz su fal, file locale
giv upscale --provider fal --video clip.mp4 --max-cost 0.50        # stima sulle misure di ffprobe
```

- **modello**: `black-forest-labs/flux-video-upscale` su openrouter (l'unico di
  ingrandimento nel catalogo al 22/11/2026), provider predefinito.
- **parametri**: `--factor` da 1,5 a 3 (default 2), `--mode` `precise` (default) o
  `creative`, prompt facoltativo; fattore e modalità vanno sempre nel corpo.
- **sorgente**: solo un URL `https` pubblico, passato al provider così com'è. Un file
  locale esce con codice 2, prima di ogni chiamata, e il messaggio propone `--provider
  fal`: nella prova del 22/11/2026 OpenRouter ha rifiutato il file mandato come data URI
  (HTTP 400, solo URL `https`, nessun addebito). Da URL giv non misura il sorgente: niente
  stima, quindi `--max-cost` esce con codice 2.
- **costo**: listino a megapixel-secondo, 0,035 $ in `precise` e 0,05 $ in `creative`
  (la formula del provider non è documentata); la stima prudente, lati del sorgente ×
  fattore² × durata, vuole le misure, che da URL mancano. Il costo vero è il `cost_usd` di
  OpenRouter nel manifest a fine job; recupero con `giv job fetch --provider openrouter <id>`.
- **fal** (`--provider fal`, chiave `FAL_KEY`): Topaz (`topaz-upscale`, modello Proteus),
  solo `--video` e `--factor` da 1 a 4 (default 2); `--mode` e il prompt sono rifiutati. Il
  file locale (mp4, al massimo 50 MiB) va sullo storage di fal come per `giv avatar`, un
  URL `https` passa così com'è. Costo a secondo del sorgente (arrotondato in eccesso) per
  fascia d'uscita, lati del sorgente × fattore: 0,01 $/s fino a 720p, 0,02 fino a 1080p,
  0,08 oltre; il doppio sopra i 30 fps, nessuna tariffa oltre i 60 fps (codice 2 con
  `--max-cost`). Servono durata, lati e fps letti con ffprobe: da URL la stima non c'è.
  fal non riporta il costo, il manifest ha la stima (`cost_est_usd`); recupero con
  `giv job fetch --provider fal <id>`.

### Personaggio che parla (`giv avatar`, fal)

```bash
giv avatar --image anja.png --audio voce/saluto.wav --dry-run "sorride e parla in camera"
giv avatar --image anja.png --audio voce/saluto.wav --model pro --max-cost 3.00
```

- **modello**: Kling AI Avatar v2 su fal (chiave `FAL_KEY`): `--model standard` (default,
  `avatar-v2-standard`) o `pro` (`avatar-v2-pro`).
- **ingressi**: `--image` file o URL http(s) (scaricato da giv), `--audio` file locale wav
  o mp3 (al massimo 25 MiB), prompt facoltativo. Immagine e voce vanno sullo storage di
  fal, con scadenza di un'ora.
- **durata**: la clip dura quanto l'audio e ne porta la voce. Oltre 60 s di audio un
  avviso (con `--strict` codice 2): fal non documenta la durata massima.
- **costo**: 0,0562 $/s (`standard`) o 0,115 $/s (`pro`) sulla durata dell'audio
  arrotondata al secondo per eccesso; fal non riporta il costo, il manifest ha la stima
  (`cost_est_usd`). Recupero con `giv job fetch --provider fal <id>`.

## Recupero dei job video

Un video è un job asincrono: `giv` lo avvia, ne controlla lo stato ogni 10 s fino a
`--timeout` e scarica il file. Se l'attesa si interrompe dopo l'avvio (timeout, rete o
errore del provider: codice 5 con l'id del job nel messaggio; oppure `giv` chiuso a mano),
il job resta sul provider e può essere già addebitato. Invece di rilanciare `giv video`:

```bash
giv job fetch --provider openrouter <id>                  # --out e --name come giv video
giv job fetch --provider xai --timeout 30m --name s2 <id>
```

- **stato su file**: ogni job avviato da `giv video` e dai comandi sulle clip lascia una
  riga in `giv-jobs.jsonl`
  nella directory corrente (id, indirizzo di attesa, modello, prompt inviato), creato con
  permessi 0600. `giv job fetch` va lanciato dalla stessa directory: su OpenRouter
  l'indirizzo di attesa arriva solo nella risposta dell'avvio.
- **nessun nuovo avvio**: `job fetch` non manda mai la richiesta di generazione. Controlla
  subito lo stato, aspetta se il job è ancora in corso e scarica il video; manifest e riga
  di `giv-log.jsonl` come `giv video` (`command` `job fetch`, `job_id`, `cost_usd` su
  openrouter).
- **job fallito**: errore con l'id del job, codice 5.
- **rifiuti con codice 2, senza richieste**: provider senza job video (openai; i job
  sono di gemini, xai, openrouter e fal), id
  assente da `giv-jobs.jsonl`, file di stato che non è un file regolare (anche un
  symlink), indirizzo di attesa fuori dall'API del provider (schema, host e porta) o
  diverso dal percorso del job (`<api>/videos/…/<id>`, senza query).
- **la chiave va solo all'API del provider**: nei redirect e negli scaricamenti, fuori da
  schema, host e porta dell'API l'intestazione `Authorization` viene tolta. I risultati
  fuori dall'API si scaricano solo da `https` verso indirizzi pubblici, coi redirect
  ricontrollati e senza `Referer`; video e immagini di xai si scaricano sempre senza
  chiave, anche quando l'indirizzo è sull'API.
- `--timeout` (di `giv video` e di `job fetch`) copre l'attesa, i controlli dello stato e
  lo scaricamento.

## Voce fuori campo: `giv voice`

```bash
giv voice --dry-run "Swerpify ti porta dove vuoi."
giv voice --pronuncia pronunce.json --speed 1.1 --name s1-voce --max-cost 0.01 "Swerpify ti porta dove vuoi."
giv voice --text-file testi/s2.txt --voice eve --lang it --out voce
giv voice --provider gemini --style "calda, ritmo calmo" --max-cost 0.02 "Swerpify ti porta dove vuoi."
giv voice --kit kit/argo.json --name s1-voce "Swerpify ti porta dove vuoi."
giv voice --voices
giv voice --voices --provider gemini
```

- **provider**: xAI (predefinito, chiave `XAI_API_KEY`), `POST /v1/tts`, oppure Gemini
  (`--provider gemini`, chiave `GEMINI_API_KEY`), `POST /v1beta/interactions` col modello
  `gemini-3.8-flash-tts` (oppure `--model gemini-3.8-flash-lite-tts`). Voci predefinite:
  `eve` per xai (scelta dopo l'ascolto) e `Kore` per gemini. Lingua predefinita `it` (`--lang` accetta un codice BCP-47 o `auto`; gemini
  la rileva dal testo e non la riceve). `--style` vale solo per gemini (stile e ritmo a
  parole): xai lo ignora con un avviso (per ritmo e pause: `--speed` e i tag nel testo,
  come `[pause]`); `--speed` vale solo per xai: gemini la ignora con un avviso.
- **testo**: fra virgolette oppure `--text-file` (UTF-8), al massimo 15.000 caratteri per
  richiesta (con gemini 6.000, pronunce comprese, perché l'audio di una richiesta non
  supera gli 11 minuti circa). Il testo va al provider (xAI conserva richieste e
  risposte per 30 giorni): solo testi di marketing, mai dati di clienti.
- **pronunce** (`--pronuncia`): un file JSON piatto `{"Swerpify": "suèrpifai", "ARGO":
  "àrgo"}`: parola intera, senza distinguere le maiuscole, vince la più lunga. A xAI va
  come dizionario; gemini non ne ha uno, quindi giv sostituisce le parole nel testo
  inviato (nei sottotitoli e nel manifest resta la grafia giusta). Al massimo 200 voci;
  la parola fino a 100 caratteri (lettere, cifre, apostrofi e spazi), la pronuncia fino
  a 128 (anche IPA fra barre, `/ˈanja/`, utile solo con xai); fuori da questi limiti, o
  con due parole uguali a meno delle maiuscole, codice 2 prima di qualsiasi chiamata.
- **kit** (`--kit <kit.json>`): il blocco `voce` del kit (provider, modello, voce, lingua,
  velocità, stile) e le sue `pronuncia` fanno da predefiniti; i flag vincono e
  `--pronuncia` va sopra le pronunce del kit (a parità di parola vince il file). Con un
  `--provider` diverso da quello del kit, del kit valgono solo lingua e pronunce (avviso).
  Il kit si controlla come con `giv kit check` (codice 2 se non va).
- **uscita**: in `--out` (default `out`) `<nome>.wav` (24 kHz) e `<nome>.tempi.json` con
  i tempi per carattere (`{"provider", "text", "duration_s", "chars": [{"c", "start",
  "end"}]}`); `--name` dà il nome, altrimenti deriva dal testo con data e ora. Un file
  che c'è già non si sovrascrive: il nome diventa `<nome>-2`, `<nome>-3`… Se i tempi
  mancano o non tornano, solo il WAV, con un avviso; gemini non dà tempi, quindi sempre
  solo il WAV (i sottotitoli si distribuiscono sulla durata). Su stdout il manifest JSON
  (file, durata misurata sul WAV, caratteri, stima), una riga in `giv-log.jsonl`.
- **costo**: xai 15 $ per milione di caratteri del testo inviato (le pronunce non cambiano
  il conto): uno spot da 375 caratteri costa circa 0,006 $. xAI non riporta il costo: il
  manifest ha la stima (`cost_est_usd`). Gemini: 0,50 $ per milione di token di testo e
  9 $ (flash-lite 6 $) per milione di token audio, 25 token al secondo; **dal 01/01/2027
  il doppio** (la tariffa si sceglie dalla data). Gemini non ha una velocità e lo stile può
  allungare il parlato: la stima (`cost_est_usd`), su cui vale `--max-cost`, è il
  **massimo per richiesta** dai limiti del modello (8.192 token di testo e 16.384 d'uscita:
  circa 0,15 $ con flash, 0,10 $ con flash-lite, il doppio dal 2027); in `cost_source`
  anche il costo probabile (10 caratteri al secondo, almeno 2 secondi);
  dopo la generazione il manifest ha anche il costo dai token riportati o, se mancano,
  dalla durata misurata (`cost_usage_usd`). `--dry-run` stampa la richiesta e la stima
  senza chiamare nessuno; `--max-cost` rifiuta con codice 2 se la stima supera il tetto
  o non è stimabile (per esempio un modello gemini senza tariffa nel listino di giv).
- **una sola richiesta, mai ripetuta**: un esito incerto (redirect, errore del server,
  rete, risposta non valida) esce con codice 5 e una riga `failed` nel registro: la voce
  può essere stata addebitata, controlla prima di rilanciare. Voce sconosciuta: codice 3
  (elenco con `giv voice --voices`).

## Rifinitura in locale: `giv post`

`giv post <operazione>` rifinisce un video che c'è già con `ffmpeg` e `ffprobe` (da
installare e nel PATH): niente rete, nessuna chiave, nessuna spesa.

```bash
giv post help                                     # operazioni disponibili; giv post <operazione> --help
giv post trim    --video spot.mp4 --start 0.5 --end 6
giv post speed   --video spot.mp4 --factor 0.5
giv post freeze  --video spot.mp4 --at 4 --duration 2
giv post reframe --video spot.mp4 --aspect 9:16 --fit crop --focus 0.4
giv post overlay --video spot.mp4 --logo img/logo.png --logo-pos alto-destra --logo-width 0.12 \
                 --title "Crema alle erbe" --from 1 --duration 3 --pos basso \
                 --end-card img/sfondo.png --end-text "Swerpify" --end-duration 3
giv post mix     --video spot.mp4 --music musica.mp3 --music-volume 0.25 --duck \
                 --voice voce.wav --voice-at 0.5 --loudness -14
```

Comune a tutte le operazioni:

- **flag comuni**: `--video <file>` (obbligatorio, file locale con una traccia video),
  `--out <dir>` (default `out`, creata se manca), `--name <nome>` (nome del file senza
  cartelle; default `<nome del video>-<operazione>`), `--dry-run` (solo controlli e piano
  in JSON, nessun file);
- **mai sovrascrivere**: l'ingresso non si modifica mai; se il nome d'uscita è già preso
  il file nuovo prende `-2`, `-3`… (leggere il percorso dal JSON); un'uscita che è il file
  in ingresso esce con codice 2. Il file compare in `--out` solo a lavoro riuscito;
- **controlli**: un valore non valido (numero fuori intervallo, `NaN`, flag vuoto, flag che
  dipende da un altro assente, posizione o colore fuori elenco) esce con codice 2 prima di
  lanciare ffmpeg; ffmpeg o ffprobe mancanti danno codice 1. Tutto entro 30 minuti;
- **uscita**: su stdout solo il JSON del risultato (`command`, per esempio `post trim`;
  `provider` `local`, `model` `ffmpeg`; `inputs` per ruolo col percorso e l'impronta;
  `params`; `files`; `duration_s`, `width`, `height`, `fps`, `video_codec`, `audio_codec`;
  `cost_usd` 0; `warnings`), avvisi ed errori su stderr; una riga a costo zero in
  `giv-log.jsonl`. Video H.264, audio AAC 48 kHz, `moov` in testa.

| Operazione | Flag | Cosa fa |
|---|---|---|
| `trim` | `--start <s>` (default 0) · `--end <s>` (default: la fine) | tiene solo il tratto fra i due istanti |
| `speed` | `--factor <0.25-4>` obbligatorio | 2 = due volte più veloce (metà durata), 0.5 = rallentato; l'audio segue alla stessa velocità senza cambiare tono (`atempo`) |
| `freeze` | `--at <s>` (default 0) · `--duration <s>` obbligatorio (fino a 60) | ferma il fotogramma a `--at` per `--duration` secondi, poi il video riprende; l'audio fa una pausa di silenzio; tempi arrotondati al fotogramma |
| `reframe` | `--aspect 9:16\|1:1\|4:5\|16:9` obbligatorio · `--fit crop\|pad\|blur` (default `crop`) · `--focus <x>[,<y>]` | cambio formato: il lato corto resta quello del video (da 1920×1080: 1080×1920, 1080×1080, 1080×1350); `crop` riempie il quadro tagliando i bordi, `pad` mette bande nere, `blur` mette il video intero sopra lo stesso video sfocato; `--focus` (solo `crop`, da 0 a 1, default 0.5,0.5) dice quale punto tenere. Nome di default `<video>-9x16.mp4`… |
| `overlay` | vedi sotto | logo, titolo e cartello finale |
| `mix` | vedi sotto | musica, voce fuori campo e volume finale |

Con `reframe` l'inquadratura è **fissa**: un soggetto che si sposta può uscire dal quadro;
controllare l'uscita (o scegliere `pad`/`blur`).

**`giv post overlay`** posa sul video, dentro i **margini sicuri** del formato (14% in
alto, 35% in basso e 6% ai lati per i 9:16, dalla guida annunci Meta per i Reels; 5% per
lato per gli altri formati):

- un **logo**: `--logo <png|jpg|webp>` (la trasparenza resta), `--logo-pos` (default
  `alto-destra`), `--logo-width` (frazione della larghezza del video, default 0.12),
  `--logo-opacity` (da 0 a 1, default 1), `--logo-from` e `--logo-to` (secondi; default
  tutto il video). Un logo che non sta nei margini esce con codice 2;
- un **titolo**: `--title "<testo>"`, `--from <s>` (default 0), `--duration <s>`
  (default 3, dentro la durata del video), `--pos` (default `basso`), con una dissolvenza
  di 0,3 s;
- un **cartello finale** accodato dopo il video, lungo `--end-duration` secondi (default
  3, al più 60; sotto il cartello l'audio è muto): `--end-color <#RRGGBB>` (sfondo, default
  nero), `--end-card <immagine>` (sfondo, contenuta nel quadro sul colore), `--end-logo
  <immagine>` (al centro, o sopra il testo), `--end-text` (al centro), `--end-subtext` (in
  basso, più piccolo; nei 9:16, dove sotto il testo non restano due righe prima del
  margine del 35%, subito sotto il testo, che sale un poco). Basta uno di questi flag per
  avere il cartello;
- per titolo e cartello: `--font <ttf|otf|ttc>` (default DejaVu Sans, se installato) e
  `--color <#RRGGBB>` (default bianco, col bordo nero).

Posizioni: `alto-sinistra`, `alto`, `alto-destra`, `sinistra`, `centro`, `destra`,
`basso-sinistra`, `basso`, `basso-destra`. I testi si scrivono **alla lettera** (graffe,
barre inverse e `\N` compresi; un a capo va a riga nuova) in un file `.ass` generato da
giv, mai nella stringa dei filtri di ffmpeg. Il JSON riporta in `params` il riquadro del
logo in pixel (`logo.box`, `end_card.logo_box`), i margini e `duration_out`.

**`giv post mix`** lavora sull'audio:

- `--music <file>` (wav, mp3, m4a, ogg, flac…): ripetuta da capo se è più corta del video,
  tagliata alla fine; `--music-volume` (moltiplicatore oltre 0 e fino a 4, default 0.25);
  `--duck` abbassa la musica mentre parla la voce (servono `--music` e `--voice`);
- `--voice <file>`: voce fuori campo sommata all'audio del video, da `--voice-at <s>`
  (default 0); la parte oltre la fine del video si taglia;
- `--loudness <LUFS>`: volume finale in LUFS integrati, da -70 a -5, misurato e corretto in
  due passate (`loudnorm`); default **-14**, la convenzione dei social (nessuna fonte
  ufficiale, quindi impostabile); `off` lascia il volume com'è.

L'audio del video resta e si somma a voce e musica (senza audio si parte dal silenzio); il
video si copia senza ricodifica se è già H.264. Serve almeno uno fra `--music`, `--voice` e
un `--loudness` diverso da `off`.

## Foglio di contatto, fotogramma e GIF: `giv sheet`, `giv frame`, `giv gif`

Tre comandi per **guardare** un video senza aprirlo, in locale con `ffmpeg` e `ffprobe`:
niente rete, nessuna spesa. Valgono le regole comuni di `giv post` (`--out`, `--name`,
`--dry-run`, mai sovrascrivere, codice 2 prima di ffmpeg, JSON su stdout e riga a costo
zero in `giv-log.jsonl`).

```bash
giv sheet spot.mp4 --frames 8 --cols 4 --labels   # foglio di contatto JPEG di un video
giv sheet clip/ --frames 4 --labels               # una riga per file della cartella
giv sheet storyboard.json --labels                # una riga per scena: fotogramma chiave e clip
giv frame --video spot.mp4 --at 1.5               # un fotogramma PNG
giv frame --video scena-1.mp4 --last              # l'ultimo fotogramma (attacco della scena dopo)
giv gif   --video spot.mp4 --start 2 --end 5 --width 480 --fps 12
```

- **`giv sheet <file|cartella|storyboard.json>`** (il file si può dare anche con
  `--video`) mette in griglia i fotogrammi e salva `<nome>-foglio.jpg`: `--every <s>` un
  fotogramma ogni s secondi da 0 (più di 0, al più 3600), oppure `--frames <n>` n
  fotogrammi a intervalli uguali (1-100); senza nessuno dei due, uno per colonna. `--cols`
  da 1 a 12 (default 4), al più 200 riquadri. `--labels` scrive su ogni riquadro
  l'istante, con una cartella anche il nome del file, con uno storyboard l'id della scena
  e la ripresa. Con una **cartella**: una riga per file, in ordine di nome (video `.mp4`
  `.m4v` `.mov` `.mkv` `.webm` `.avi` `.ts`, immagini `.png` `.jpg` `.jpeg` `.webp`; solo
  file normali, i link si saltano con un avviso). Con uno **storyboard**: una riga per
  scena, col fotogramma chiave se c'è e i fotogrammi della clip in uso (`giv story run`) o
  del file della scena; le scene senza clip né fotogramma chiave si saltano con un avviso.
  In `params`: `mode` (`file`, `cartella`, `storyboard`), `cols`, `rows`, `cells`, le misure
  dei riquadri e del foglio, `scenes` con uno storyboard.
- **`giv frame --video <file>`** salva un fotogramma in PNG (`<nome>-fotogramma.png`):
  `--at <s>` (dall'inizio, entro la durata) oppure `--last` (l'ultimo), uno solo dei due;
  `--width <px>` (16-7680, altezza in proporzione) ne fa una miniatura.
- **`giv gif --video <file>`** fa una GIF animata ripetuta all'infinito (`<nome>-gif.gif`,
  palette calcolata in due passate) dal tratto fra `--start` (default 0) e `--end`
  (default la fine), al più 60 s; `--width` da 16 a 1920 (default 480, mai oltre la
  larghezza del video: in quel caso un avviso), `--fps` da 1 a 30 (default 12).

## Esportazione per canale: `giv export`

`giv export` prende un video finito e scrive **un file per canale** col suo profilo, in
locale con `ffmpeg` (niente rete, nessuna spesa; regole comuni di `giv post`):

```bash
giv export --video spot.mp4 --for instagram,tiktok,youtube,linkedin
giv export --video spot.mp4 --for instagram:4:5,youtube:9:16 --fit blur --loudness -16
```

- `--for <canali>` (obbligatorio): `instagram`, `tiktok`, `youtube`, `linkedin` separati da
  virgole, ognuno col formato facoltativo `<canale>:<formato>`. Formato predefinito: 9:16
  per Instagram (Reel) e TikTok, 16:9 per YouTube e LinkedIn; YouTube in 9:16, 1:1 o 4:5 è
  uno Short.
- `--fit crop|pad|blur` e `--focus <x>[,<y>]` come in `giv post reframe`;
  `--loudness <lufs>|off` come in `giv post mix` (default -14 LUFS, picco -1 dBTP).
- Misure 1080×1920, 1080×1080, 1080×1350, 1920×1080; H.264 High, AAC 48 kHz stereo,
  `moov` in testa. Fotogrammi al secondo: 30 per Instagram e TikTok; per YouTube quelli
  del video (altrimenti 30); per LinkedIn quelli del video se fra 10 e 60 (altrimenti 30).
- Durata: Instagram da 3 s a 15 min, TikTok fino a 10 min, YouTube fino a 15 min (Shorts
  3 min), LinkedIn da 3 s a 10 min. Un video fuori dai limiti di un canale esce con
  codice 2 prima di scrivere qualsiasi file.
- File `<nome>-<canale>-<formato>.mp4` in `--out` (es. `spot-instagram-9x16.mp4`;
  `<nome>` è `--name` o il nome del video), una riga a costo zero in `giv-log.jsonl` per
  file. Il JSON ha `command` `export`, `files` ed `exports[]` con `channel`, `aspect`,
  `file` (con `--dry-run` `output`), misure, codec, `loudness_lufs` e `convention`.

I valori che non vengono da una specifica ufficiale del canale (i -14 LUFS, le misure del
feed di Instagram 4:5 e 1:1, il formato di TikTok) sono **convenzioni**: il manifest le
elenca in `exports[].convention`.

## Storyboard: video a più scene

Uno storyboard è un file JSON con le scene di un video da montare. `giv story check` lo
valida e ne stima il costo **senza generare niente**: legge solo i file locali e il
catalogo video del provider (gratuito, ma serve la chiave). L'esempio in `examples/spot/`
passa il controllo:

```bash
giv story check examples/spot/storyboard.json     # JSON su stdout: scene, stime, totale
giv story help                                    # aiuto; giv story run --help per un sottocomando
```

Per tutti i sottocomandi su stdout va solo il JSON del risultato; avanzamento, avvisi ed
errori vanno su stderr. `giv story help` (o `giv story --help`) e `giv story <sottocomando>
--help` stampano l'aiuto su stderr ed escono con 0.

```json
{
  "titolo": "Spot di prova: crema alle erbe, 18 secondi",
  "formato": { "aspect": "16:9", "risoluzione": "720p", "fps": 24 },
  "default": { "provider": "openrouter", "modello": "alibaba/wan-3.0", "audio": "off" },
  "tetto_costo_usd": 3,
  "riferimenti": { "prodotto": ["img/prodotto.png"] },
  "scene": [
    { "id": "s1", "tipo": "video", "durata": 5, "immagine": "img/prodotto.png",
      "preset": ["dolly-in"], "fuoco": 0.4,
      "prompt": "il vasetto di crema su un piano di legno chiaro…", "voce": "voce/s1.wav" },
    { "id": "s2", "tipo": "video", "durata": 5, "continua_da": "s1", "preset": ["orbit"],
      "prompt": "la camera gira attorno al vasetto…", "voce": "voce/s2.wav" },
    { "id": "s3", "tipo": "video", "durata": 5, "usa": ["prodotto"], "preset": ["static"],
      "taglio": { "da": 0.5 }, "prompt": "una mano apre il vasetto e mostra la crema…" },
    { "id": "chiusura", "tipo": "immagine", "durata": 3, "file": "img/logo.png",
      "voce": "voce/chiusura.wav" }
  ],
  "montaggio": {
    "transizione": "dissolvenza", "uscita": "spot.mp4",
    "formati": ["16:9", "9:16"], "adatta": "ritaglio",
    "musica": { "file": "musica/musica.wav", "volume": 0.2, "sotto_voce": true, "sfuma_fine": 2 },
    "logo": { "file": "img/logo.png", "posizione": "alto-destra", "larghezza": 0.12 },
    "titoli": [ { "testo": "Crema alle erbe", "scena": "s1", "da": 0.5, "durata": 3 } ],
    "cartello_finale": { "durata": 3, "sfondo": "#1E3A2B", "testo": "Crema alle erbe",
                         "sottotesto": "in erboristeria" },
    "stile_testi": { "colore": "#FFFFFF", "bordo": "#000000" },
    "volume": -14
  }
}
```

Stima dell'esempio: 3 scene da 5 s a 720p su `alibaba/wan-3.0` (0,10 $/s), `cost_total_usd`
1,5 (il `taglio` di s3 cambia solo il montaggio, non la durata chiesta al modello). I file in
`examples/spot/img/`, `voce/` e `musica/` sono segnaposto (tinte piatte e silenzio):
sostituirli con la foto del prodotto, il logo, la voce e la musica vere.

| Campo | Cosa contiene |
|---|---|
| `titolo` | facoltativo |
| `formato` | `aspect`, `risoluzione` e `fps` del video: valgono per le scene video che non li indicano |
| `default` | `provider`, `modello`, `risoluzione`, `aspect`, `audio` (`on`/`off`), `seed`, `tutela`, `param` (oggetto, come `--param`) per tutte le scene video; senza `modello` vale il default di `giv video` per quel provider; senza `provider` (qui o nella scena) vale `GIV_PROVIDER`, altrimenti codice 2 |
| `tetto_costo_usd` | tetto in dollari: un totale oltre il tetto, o una generazione senza stima né costo dichiarato, esce con codice 2 |
| `strict` | come `--strict` di `giv video`: anche i controlli di rifinitura sono errori |
| `riferimenti` | insiemi di immagini per nome (`"prodotto": ["img/prodotto.png"]`), richiamati dalle scene con `usa` |
| `scene` | le scene, in ordine |
| `avatar` | `modello` (`standard`, default, o `pro`) delle scene avatar che non lo indicano |
| `montaggio` | `transizione` (`taglio`, default, o `dissolvenza`), `musica` (`file`, `volume` da 0 a 1, `sotto_voce`, `sfuma_fine`), `sottotitoli` (file), `uscita` (file finale); dalla 0.11.0 `formati`, `adatta`, `logo`, `titoli`, `cartello_finale`, `stile_testi`, `volume`: vedi [Montaggio](#montaggio-giv-story-cut) |
| `foglio_contatto` | `false` perché `giv story run` e `giv story cut` non scrivano `foglio-contatto.jpg` accanto allo storyboard (default: lo scrivono) |

Ogni scena ha un `id` (lettere, cifre, `_` e `-`, al massimo 64 caratteri), un `tipo`, una
`durata` in secondi e, facoltativa, la `voce`: un file audio fuori campo, messo al
montaggio. `ritardo_voce` (secondi, anche decimali, finito e non negativo; solo con
`voce`) la fa partire dopo l'inizio della scena; senza vale 0.

- `video`: generata da un modello. `prompt`, `negative`, `preset`; il primo fotogramma
  viene da **uno solo** fra `immagine` (file o URL), `fotogramma_chiave` (immagine
  generata prima con `giv image`: `prompt`, `provider`, `modello`, `costo_dichiarato_usd`;
  gli insiemi di `usa` ne sono gli ingressi) e `continua_da` (id di una scena video o clip
  precedente, di cui si usa l'ultimo fotogramma); poi `ultimo_fotogramma`, `usa`,
  `costo_dichiarato_usd` e i campi di `default`, che la scena sovrascrive. Senza `durata`
  vale il default del modello. Con fal `kling-4.0`, `fotogrammi_intermedi` (file o URL, in
  ordine, come `--keyframe`); con `kling-4.0` e `kling-v3-pro`, `inquadrature` (elenco di
  `{"prompt": "…", "durata": 3}`, come `--shot`): il testo sta tutto lì, quindi niente
  `prompt` né `preset`, e la scena dura la somma (una `durata` diversa esce con codice 2).
  Con un altro modello i due campi escono con codice 2.
- `tutela` (nel blocco `default` o nella scena video, che lo sovrascrive): la clausola che
  si aggiunge al prompt coi preset, come `--tutela` di `giv video`: `prodotto`,
  `illustrazione` (o una clausola dei file di preset dell'utente) oppure `no`. Senza
  indicazione vale il default di `giv video`: `prodotto` per una scena con preset e un
  primo fotogramma (`immagine`, `fotogramma_chiave` o `continua_da`). Per una mascotte o
  un'illustrazione serve `"tutela": "illustrazione"`. Un valore sconosciuto esce con
  codice 2.
- `clip`: un `file` video già pronto (ripresa vera, screencast).
- campi di montaggio di una scena (facoltativi, usati solo da `giv story cut`): `fuoco`, il
  punto da tenere quando il montaggio ritaglia la scena per un altro formato (`"adatta":
  "ritaglio"`): un numero da 0 (sinistra) a 1 (destra) oppure `{"x": …, "y": …}` (y da 0 in
  alto a 1 in basso), default 0.5 cioè il centro; per le scene `video`, `clip` e `avatar`
  anche `taglio` (`{"da": 0.5, "a": 4}`: il pezzo della clip da usare, in secondi dall'inizio
  del file; senza `a` fino alla fine) e `velocita` (da 0.25 a 4, 2 = due volte più veloce,
  l'audio della clip segue). Con `taglio` o `velocita` la scena dura nel montaggio quanto
  il pezzo usato diviso la velocità; `durata` resta la lunghezza chiesta al modello.
- `immagine`: un `file` immagine fermo, con `durata` obbligatoria (cartelli, chiusure).
- `avatar`: un personaggio che parla, generato da `giv story run` come `giv avatar` (fal).
  `immagine` (un file della cartella dello storyboard) e `voce` obbligatorie; poi
  `prompt`, `modello` (`standard` o `pro`; senza, quello del blocco `avatar`, poi
  `standard`), `provider` (solo `fal`) e `costo_dichiarato_usd`. La scena dura quanto la
  voce, che guida il labiale dall'inizio e resta nella clip: niente `durata`,
  `ritardo_voce` né `file`. Con `tetto_costo_usd` serve la durata della voce misurata con
  ffprobe (un costo dichiarato non basta).

`giv story check` esce con codice 2 e l'elenco di tutti i problemi trovati, uno per riga:

- **forma**: JSON con campi sconosciuti, id mancanti o ripetuti, tipi sconosciuti, campi
  che non valgono per il tipo di scena, `continua_da` verso una scena successiva o
  inesistente, più di un primo fotogramma;
- **file**: relativi alla cartella dello storyboard, esistenti, regolari e dentro la
  cartella anche seguendo i link; un URL solo per `immagine`, `ultimo_fotogramma`,
  `fotogrammi_intermedi` e `riferimenti`;
- **scene video**: ognuna come `giv video --dry-run`, sul catalogo letto in quel momento
  (un modello che non c'è più viene rifiutato), col provider della scena, del blocco
  `default` o di `GIV_PROVIDER` (senza nessuno, codice 2), con capacità, preset e stima; gli avvisi
  sono nei `warnings` della scena; le scene avatar come `giv avatar --dry-run`;
- **voce** (con `ffprobe` nel PATH): sulla linea del tempo del montaggio (durate delle
  scene, dissolvenze, `ritardo_voce`) la voce di una scena deve finire prima che parta la
  voce della scena successiva che ne ha una, l'ultima prima della fine del montaggio.
  Oltre 0,05 s un avviso, nei `warnings` della scena e su stderr, dice di quanti secondi e
  su quale scena; una voce che supera la sua scena senza toccarne un'altra non ha avviso.
  Un avviso anche per un file che non si legge come audio;
- **costi**: `cost_est_usd` (stime da listino), `cost_declared_usd` (costi dichiarati
  dall'autore dove la stima non c'è, per esempio i modelli a token come Seedance),
  `cost_total_usd`; le generazioni senza stima né costo dichiarato (compresi i
  fotogrammi chiave) sono in `not_estimated` e non contano come zero. Con
  `tetto_costo_usd` vengono rifiutate.
- **spesa di scene tolte**: se lo stato di `giv story run` (`<nome>.stato.json`) ha spesa
  registrata per scene che non sono più scene video dello storyboard (tolte, rinominate o
  cambiate di tipo), è a parte in `removed_scenes` (`id`, `spent_usd`) e
  `removed_spent_usd`, con un avviso su stderr: non è fra le scene, ma col tetto conta
  insieme a `cost_total_usd`.

Il JSON su stdout ha `storyboard`, `titolo`, le `scenes` (per scena il piano come `giv
video --dry-run`), i costi qui sopra e `cap_usd`.

### Generazione: `giv story run`

```bash
giv story run examples/spot/storyboard.json              # genera le scene video, a pagamento
giv story run --scene s2 examples/spot/storyboard.json   # rifà solo la scena s2
giv story run --scene s2 --new-keyframe examples/spot/storyboard.json
                                                         # rifà s2 con un fotogramma chiave nuovo
```

`giv story run` genera **a pagamento** le scene `video` e `avatar` dello storyboard, in
ordine e una richiesta alla volta: prima l'eventuale `fotogramma_chiave`, poi la clip; per
`continua_da` l'ultimo fotogramma della scena prima, estratto con ffmpeg. Prima del primo
avvio rifà i controlli di `giv story check` sul catalogo letto in quel momento: un
problema esce con codice 2 senza avviare niente. I `fotogrammi_intermedi` da URL si
scaricano e si controllano prima del fotogramma chiave: uno non valido ferma la scena
senza spesa. Le clip vanno in `clip/` nella cartella
dello storyboard (`<id>-r<N>-<data>.mp4`, mai sovrascritte) e ogni clip entra nel
registro di `giv log` con `command` `story video` (`story avatar` per le scene avatar).
Alla fine riscrive `foglio-contatto.jpg` accanto allo storyboard, come `giv sheet
storyboard.json --labels` (salvo `"foglio_contatto": false`; un problema col foglio è solo
un avviso).
L'attesa di ogni video ha un tetto di 15 minuti.

**Stato.** Lo stato delle scene sta in un solo file accanto allo storyboard,
`<nome>.stato.json` (per `storyboard.json`, `storyboard.stato.json`); mentre il comando
lavora c'è anche `<nome>.stato.lock` e un secondo `giv story run` sullo stesso storyboard
esce con codice 2. Spostare, rinominare o copiare la cartella dello spot non cambia
niente. Una nuova esecuzione riprende da dove si era fermata: prima di ogni nuovo avvio
recupera i job già partiti, **senza rilanciarli**, poi genera le scene mancanti. Uno stato
illeggibile o non valido esce con codice 2, senza avvii.

| `status` | Cosa vuol dire | Alla prossima esecuzione |
|---|---|---|
| `fatto` | clip scaricata | niente; se il file della clip non c'è più, la scena si rigenera |
| `partito` | job avviato, video non ancora scaricato (rete, timeout, errore di salvataggio) | il job si recupera, mai rilanciato |
| `incerto` | richiesta inviata senza esito (5xx, rete caduta dopo l'invio, interruzione): può essere stata addebitata | codice 2 finché non la si rifà con `--scene <id>`, dopo aver controllato sul provider |
| `fallito` | job finito male sul provider, spesa contata | non si rigenera da sola: le altre scene proseguono e il comando esce con codice 1 e la riga `giv story run --scene <id>` per rifarla |
| `da_fare` | niente in corso: avvio rifiutato subito (4xx) o solo fotogramma chiave fatto | si riprova |

`--scene <id>` rifà una sola scena video, anche fatta, incerta o fallita: la ripresa
precedente resta nello stato e in `clip/`. Se la scena ha un job ancora da scaricare, il
job recuperato vale come nuova ripresa, senza un nuovo avvio.

**Job che non si scarica più.** `--scene <id> --new-job` abbandona il job già partito della
scena (per esempio scaduto sul provider): il job non si recupera, passa fra le riprese
precedenti (`previous`, con `kind` `job` e l'ultimo errore) e il suo addebito resta
contato nel tetto; ne parte uno nuovo, con la stessa riserva e lo stesso tetto. Il job
lasciato si controlla come quelli da recuperare (provider, id, indirizzo di attesa
sull'API del provider): se non è valido, codice 2 e nessun avvio. Vale solo con
`--scene`; se non parte nessuna richiesta lo stato non cambia.

**Fotogramma chiave.** Una volta generato, il `fotogramma_chiave` di una scena si riusa a
ogni ripresa (con `--scene` un avviso su stderr lo ricorda) e non si rigenera mai da solo,
nemmeno se il suo file non c'è più: in quel caso la scena esce con codice 2, senza
richieste, e il messaggio indica il comando da usare. Per un fotogramma chiave nuovo (a
pagamento, venuto male o cancellato) `--scene <id> --new-keyframe`: vale solo insieme a
`--scene` e per una scena con `fotogramma_chiave` (altrimenti codice 2), passa dalla
stessa riserva e dallo stesso tetto delle altre richieste e il fotogramma precedente resta
nello stato fra le riprese precedenti (`previous`, con `kind` `fotogramma`). Se la scena
ha un job già partito, quello si recupera e il fotogramma non si rigenera (avviso).

**Tetto di spesa.** Con `tetto_costo_usd` il conto è cumulativo sullo stato: prima di ogni
richiesta a pagamento (fotogramma chiave o avvio video) `giv story run` somma la spesa già
registrata (l'addebito riportato dal provider dove c'è, altrimenti la stima; per gemini il
costo dai token riportati, se non è più basso della stima) e le stime delle generazioni
ancora da fare; oltre il tetto esce con codice 2 senza nuovi avvii. Ogni
richiesta viene registrata nello stato prima di partire; se lo stato col costo reale del
fotogramma chiave non si salva, la scena si ferma prima del video. La spesa registrata comprende
anche le scene non più nello storyboard: conta nel tetto, ma è mostrata a parte (avviso su
stderr, `removed_scenes` e `removed_spent_usd`, e nel messaggio di rifiuto del tetto).
**Cancellando il file di stato il conto riparte da zero** e le scene tornano da generare.

Il JSON su stdout ha `state`, le `scenes` (per scena `status`, `file`, `keyframe`, `takes`:
le riprese video, `this_run`: generata, recuperata, già fatta o fallita, `cost_est_usd`,
`cost_usd`), `jobs_started`, `jobs_recovered`, `keyframes` e i totali `cost_est_usd`
(stime e costi dichiarati delle scene), `cost_usd` (addebito riportato dai provider),
`spent_usd` (la spesa contata per il tetto, comprese le scene tolte), `removed_scenes` e
`removed_spent_usd` (le scene tolte, se ce ne sono) e `cap_usd`.

### Montaggio: `giv story cut`

```bash
giv story cut examples/spot/storyboard.json       # JSON su stdout: file scritti, durata, formato
giv story cut --formato 9:16 examples/spot/storyboard.json          # solo spot-9x16.mp4
giv story cut --export instagram,youtube examples/spot/storyboard.json
                                                  # in più un file per canale dal montato
```

`giv story cut` monta in un video le scene già pronte, con `ffmpeg` e `ffprobe` (da
installare e nel PATH, per esempio `apt install ffmpeg`): niente rete, niente generazione,
nessuna chiave. Ogni scena dà al montaggio:

- `video`: la clip in uso nello stato di `giv story run` (`<nome>.stato.json`); se una
  manca esce con codice 2 e l'elenco delle scene da generare, col loro stato;
- `avatar`: la clip in uso nello stato, con la sua voce (non si aggiunge una seconda volta);
- `clip`: il suo `file`; `immagine`: il suo `file` fermo per tutta la `durata`.

Come monta:

- **formato**: `aspect`, `risoluzione` e `fps` da `formato`, poi dal blocco `default`,
  altrimenti 16:9, 720p e 30 fps. La risoluzione è il lato corto: 720p è 1280×720 in 16:9
  e 720×1280 in 9:16. Video H.264, audio AAC.
- **transizioni**: `taglio` o `dissolvenza` di 0,5 s fra una scena e l'altra; ogni
  dissolvenza accorcia il totale di 0,5 s.
- **audio**: la `voce` di una scena parte con la scena, o `ritardo_voce` secondi dopo (se
  dura di più continua sulla scena dopo, o viene tagliata alla fine); l'audio di una clip resta se il file ne ha uno;
  la `musica` va in loop sotto tutto il video a `volume` 0,25 se non indicato (0 = niente
  musica).
- **sottotitoli**: un file `.ass`, `.ssa` o `.srt`, inciso nel video. Con `ritardo_voce`
  le righe che iniziano in una scena si spostano dello stesso ritardo, in una copia (il
  file dei sottotitoli non cambia). Con `montaggio.formati` o `--formato` un `.srt` sta
  dentro i margini sicuri del formato (nel 9:16 sopra il 35% in basso, coperto dai Reels);
  un `.ass` o `.ssa` resta col suo stile.
- **uscita**: `montaggio.uscita`, altrimenti `<nome dello storyboard>.mp4`, sempre dentro
  la cartella dello storyboard anche seguendo i link. Si riscrive a ogni `giv story cut`;
  il file compare solo a montaggio riuscito. Tutto il montaggio sta entro 30 minuti.
  Un'uscita non sostituisce mai lo storyboard, il suo stato, i file che nomina né le clip
  generate (codice 2 prima di montare).
- **foglio di contatto**: dopo il montaggio riscrive `foglio-contatto.jpg` accanto allo
  storyboard (una riga per scena con id e ripresa, come `giv sheet`), salvo
  `"foglio_contatto": false`.

Dalla 0.11.0 il blocco `montaggio` ha anche questi campi, tutti facoltativi (senza, il
montaggio resta quello di prima):

- **`formati`**: i formati da montare, un file per formato (`"16:9"`, `"9:16"`, `"1:1"`,
  `"4:5"`): `<uscita>-16x9.mp4`, `<uscita>-9x16.mp4`… Nessun formato predefinito: senza il
  campo un solo file nel formato dello storyboard, come prima. Il lato corto resta quello
  della `risoluzione` (720p in 9:16 è 720×1280). `--formato <r>` monta solo quel formato
  (che deve essere fra `formati`, se ci sono).
- **`adatta`**: come una scena entra in un formato diverso dal suo: `bande` (default,
  bande nere), `ritaglio` (riempie il quadro tenendo il `fuoco` della scena) o `sfocato`
  (la scena intera sopra la stessa sfocata).
- **`logo`**: `file` (`.png`, `.jpg` o `.webp` nella cartella, la trasparenza resta),
  `posizione` (default `alto-destra`; le posizioni di `giv post overlay`), `larghezza`
  (frazione della larghezza del quadro, default 0.12), `opacita` (default 1), `da` e `a`
  (secondi dall'inizio del montato; default dall'inizio alla fine delle scene, non sul
  cartello). Sta dentro i margini sicuri di ogni formato, come in `giv post overlay`.
- **`titoli`**: elenco (al più 50) di `{"testo": …}` con `scena` (id: i tempi contano
  dall'inizio di quella scena; senza, dall'inizio del montato), `da` (secondi, default 0),
  `durata` (default 3), `posizione` (default `basso`) e `stile` (al posto di
  `stile_testi`), con una dissolvenza di 0,3 s. Un titolo in alto con il logo in alto
  (anche di default) nei formati verticali e quadrati (9:16, 4:5, 1:1) può finire sotto
  il logo: `giv story check` e `giv story cut` lo avvisano (nessuno spostamento).
- **`cartello_finale`**: accodato dopo l'ultima scena a taglio netto, senza audio suo (la
  musica continua): `durata` (default 3, al più 60), `sfondo` (`#RRGGBB`, default nero),
  `logo` (immagine al centro), `testo` e `sottotesto` (in basso; nei 9:16 subito sotto il
  testo, come in `giv post overlay`); serve almeno uno fra logo, testo e sottotesto.
- **`stile_testi`**: stile di titoli e cartello: `font` (file `.ttf`, `.otf` o `.ttc`
  nella cartella dello storyboard; default DejaVu Sans, se installato), `colore`
  (`#RRGGBB`, default bianco), `bordo` (colore del contorno, default nero, o `"nessuno"`),
  `dimensione` (altezza dei caratteri in frazione dell'altezza del quadro, da 0.01 a
  0.25).
- **`musica`**: oltre a `file` e `volume`, `sotto_voce: true` abbassa la musica mentre
  parlano le voci (serve almeno una scena con `voce`) e `sfuma_fine` sono i secondi di
  dissolvenza in chiusura (da 0 a 60).
- **`volume`**: volume finale in LUFS integrati, da -70 a -5 (es. -14, la convenzione dei
  social), misurato e corretto in due passate; senza il campo il volume resta com'è.

Testi di titoli e cartello: alla lettera (al più 2000 byte, UTF-8), in un file `.ass`
generato da giv, mai nella stringa dei filtri di ffmpeg. Font, logo e musica devono stare
nella cartella dello storyboard.

`--export <canali>` (come `giv export --for`: `instagram`, `tiktok`, `youtube`,
`linkedin`, anche `<canale>:<formato>`) esporta dopo il montaggio un file per canale dal
montato nel formato del canale: `<uscita>-<canale>-<formato>.mp4` (es.
`spot-instagram-9x16.mp4`), col profilo di [`giv export`](#esportazione-per-canale-giv-export),
ritaglio al centro e volume `montaggio.volume` o -14 LUFS. Il formato del canale deve
essere fra quelli montati, altrimenti codice 2 prima di montare.
- **cartella di lavoro**: privata, nella cartella temporanea del sistema (come per le
  scene avatar di `giv story run`). Se `TMPDIR` o una cartella sopra è scrivibile da altri
  utenti senza sticky bit, `giv` si ferma prima e chiede di impostare `TMPDIR` su una
  cartella propria.

Il JSON su stdout ha `storyboard`, `output`, `scenes`, `duration_s` (letta dal file
scritto), `expected_duration_s` (dalle durate delle scene), `width`, `height`, `fps`,
`video_codec`, `audio_codec` e `warnings` (per esempio una clip più corta della sua
durata o una voce tagliata); con `--export`, `exports` come in `giv export`. Con
`formati` o `--formato` il JSON ha `storyboard`, `scenes`, `formats` (un risultato come
sopra per formato, con `format`) ed `exports`. Esce con codice 2 per clip mancanti, file illeggibili,
storyboard o formato non validi e uscita fuori dalla cartella; con codice 1 se mancano
`ffmpeg` o `ffprobe` o il montaggio fallisce.

## Kit di marca e personaggi

Il kit è un file JSON (di solito `kit.json`) scritto una volta per la marca: colori, logo,
font, cartello finale, musica, immagini e descrizione dei personaggi, voce e pronunce.
`giv kit check` lo controlla ed elenca i file con le misure, **senza generare niente**:
niente rete, niente chiavi, gratuito, e non scrive niente (nemmeno in `giv-log.jsonl`).
L'esempio in `examples/kit/` (logo, personaggio e musica segnaposto, font libero DejaVu
Sans Bold con la sua licenza) passa il controllo:

```bash
giv kit check examples/kit/kit.json     # JSON su stdout: file con le misure, personaggi, voce
giv kit help                            # aiuto; anche giv kit check --help
```

```json
{
  "marca": {
    "colori": { "primario": "#1E3A2B", "testo": "#FFFFFF" },
    "logo": { "file": "logo.png", "posizione": "alto-destra", "larghezza": 0.12 },
    "stile_testi": { "font": "font/DejaVuSans-Bold.ttf" },
    "cartello_finale": { "durata": 3, "logo": "logo.png", "testo": "Marca di prova" },
    "musica": { "file": "musica/base.wav", "volume": 0.25, "sotto_voce": true },
    "volume": -14
  },
  "personaggi": {
    "mascotte": {
      "riferimenti": ["personaggi/mascotte-fronte.png", "personaggi/mascotte-profilo.png"],
      "descrizione": "Mascotte di prova: segnaposto da sostituire…",
      "tutela": "illustrazione"
    }
  },
  "voce": { "provider": "xai", "voce": "eve", "lingua": "it", "velocita": 1.0 },
  "pronuncia": { "Swerpify": "suèrpifai" }
}
```

- **`marca`**: `logo`, `stile_testi`, `cartello_finale` e `musica` hanno gli stessi campi
  e gli stessi limiti del blocco `montaggio` dello storyboard; `volume` è il volume finale
  in LUFS (da -70 a -5). `colori.primario` (sfondo del cartello e bordo dei testi) e
  `colori.testo` (colore dei testi) sono colori `#RRGGBB`.
- **`personaggi`**: per nome (lettere, cifre, `_` e `-`, al massimo 64), un insieme di
  immagini di riferimento (`riferimenti`, da 1 a 50 file `.png`, `.jpg` o `.webp`, senza
  ripetizioni), una `descrizione` (al più 2000 byte) e una `tutela` (`prodotto`,
  `illustrazione`, `no` o una clausola dei preset).
- **`voce`**: `provider` (`xai` o `gemini`), `modello`, `voce`, `lingua` (es. `it`),
  `velocita` (da 0,7 a 1,5) e `stile` (a parole).
- **`pronuncia`**: al più 200 parole, ognuna con la sua pronuncia (al più 100 byte
  ciascuna, su una riga).
- Serve almeno uno fra `marca`, `personaggi`, `voce` e `pronuncia`; un campo sconosciuto,
  un valore fuori dai limiti o un file del kit oltre 1 MiB escono con codice 2.

**File.** Il kit è in sola lettura e chiuso nella sua cartella (quella del file JSON):
ogni file è un percorso relativo a quella cartella e non ne esce, nemmeno seguendo i
link; solo file normali (niente cartelle, FIFO o device), niente URL. Un file fuori dalla
cartella, assoluto, mancante o un link che porta fuori escono con codice 2, con tutti i
problemi trovati, uno per riga. Le immagini si leggono alla lettera, mai come sequenza.

**Cosa controlla `giv kit check`.** Logo e riferimenti devono essere immagini vere
(riconosciute dai byte, al più 20 MiB; con `ffprobe` anche le misure) e la musica deve
avere una traccia audio (durata con `ffprobe`): altrimenti codice 2. Senza `ffprobe` le
misure non si leggono e c'è un avviso. Avvisi, senza cambiare il codice d'uscita: logo
senza trasparenza (PNG senza alfa, JPEG), font non leggibile (`giv story cut` lo
rifiuterebbe), più riferimenti di quanti ne accettano i modelli video con limiti noti
(xAI, Gemini, fal), descrizione oltre 500 caratteri, `velocita` con Gemini (che non ha
una velocità numerica).

Il JSON su stdout ha `kit`, `files` (per file `field`, `file`, `kind` fra `logo`, `font`,
`musica` e `riferimento`, `bytes`, e secondo il tipo `width`, `height`, `alpha`,
`font_family`, `duration_s`, `codec`), `characters` (`name`, `references`, `tutela`,
`description_chars`), `voice`, `pronunciations` (quante parole) e `warnings`; gli avvisi
vanno anche su stderr.

## Uso da un agente (Claude Code)

La skill operativa è nel repo e si installa con un symlink:

```bash
ln -s "$(pwd)/skills/gen-image-video" ~/.claude/skills/gen-image-video
```

Da quel momento qualsiasi sessione sa usare il CLI: contratto di output, matrice
provider, ricette (reference di stile, image-to-video, spot da foto di prodotto, mascotte
fra scene, preset di camera, video con Gemini, Kling 4.0 con fotogrammi intermedi e
inquadrature, spot a più scene con lo storyboard, voce fuori campo,
allungare, modificare e ingrandire una clip, personaggio che parla, rifinitura in locale
con `giv post`, foglio di contatto e fotogrammi, esportazione per canale),
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
internal/provider/gemini/ adapter Gemini (immagini con generateContent, video, extend ed edit con interactions)
internal/provider/openaicompat/  adapter OpenAI-compatible (openai, xai, openrouter) e richieste protette (guard.go)
internal/provider/fal/    adapter fal (coda, storage, schede video e avatar)
internal/caps/            capacità e prezzi dei modelli video (OpenRouter /videos/models, tabelle xAI e Gemini, schede fal) e delle operazioni sulle clip, controlli e stima
internal/presets/         libreria dei preset (presets.json incorporato) e composizione del prompt
internal/jobs/            stato dei job video avviati (giv-jobs.jsonl), per giv job fetch
internal/story/           formato, controlli e stato (<nome>.stato.json) dello storyboard di giv story
internal/kit/             kit di marca e personaggi: lettura, controlli e percorsi chiusi nella cartella del kit
internal/montage/         ffmpeg e ffprobe: montaggio di giv story cut e operazioni locali (giv post…, sheet, frame, gif, export)
internal/output/          salvataggio media + manifest JSON
skills/gen-image-video/   skill agent-native
examples/spot/            storyboard d'esempio con file segnaposto
examples/kit/             kit di marca e personaggi d'esempio con file segnaposto
```

## Versioning

SemVer, storia in [CHANGELOG.md](CHANGELOG.md). La versione del binario è in
`cmd/giv/main.go` (`const version`) e va aggiornata insieme al changelog a ogni release.
