# Changelog

Tutte le modifiche rilevanti del progetto, nel formato
[Keep a Changelog](https://keepachangelog.com/it-IT/1.1.0/);
versioning [SemVer](https://semver.org/lang/it/).

## [0.10.0] - in preparazione

### Aggiunto

- **`giv extend` e `giv edit`** (xAI, `grok-imagine-video`): allungano una clip di 2-10 s
  (`--duration`, default 6) o la modificano col prompt. `--video` è un file mp4 locale
  (al massimo 50 MiB, caricato su xAI con scadenza di un'ora) o un URL `https` che finisce
  in `.mp4`. Limiti del sorgente (2-15 s per extend, fino a 8,7 s per edit) controllati
  con ffprobe; stima sul caso più caro finché non è verificato; il manifest riporta il
  costo reale di xAI (`cost_usd`).
- **`giv upscale`** (OpenRouter, `black-forest-labs/flux-video-upscale`): `--factor` 1,5-3
  (default 2), `--mode precise|creative` (default `precise`), prompt facoltativo; un file
  locale va come data URI (non documentato da OpenRouter: se lo rifiuta serve un URL
  `https`). Stima a megapixel-secondo sulle misure del sorgente.
- **`giv avatar`** (fal, Kling AI Avatar v2): un personaggio che parla da `--image` e
  `--audio` (wav o mp3 locale), `--model standard|pro`; la clip dura quanto l'audio. Stima
  sulla durata dell'audio; oltre 60 s un avviso (con `--strict` codice 2).
- I comandi sulle clip hanno i controlli di `giv video` prima di ogni chiamata, `--dry-run`,
  `--max-cost`, `--strict`, `--timeout`, manifest con `command` e `inputs` e job
  recuperabili con `giv job fetch`. Senza `--provider` vale il provider dell'operazione.
- **Provider fal** (chiave `FAL_KEY`): `giv video --provider fal` con le schede
  `kling-v3-pro` (default) e `seedance-2.0-ref`, capacità, regole e prezzi fissati in giv
  (`giv models --provider fal --kind video --caps`); coda di fal con avvio non ripetuto,
  immagini locali caricate sullo storage di fal con scadenza di un'ora, `giv job fetch
  --provider fal`.
- **Storyboard**:
  - `ritardo_voce` per scena: la voce parte dopo l'inizio della scena; `giv story cut`
    ritarda voce e righe dei sottotitoli (in una copia del file);
  - `giv story check` avvisa quando una voce si sovrappone a quella della scena successiva
    o supera la fine del montaggio, sulla linea del tempo con durate, dissolvenze e
    ritardi;
  - scene `avatar` (immagine e voce, modello `standard` o `pro`, blocco `avatar` per il
    default), generate da `giv story run` con fal con la stessa riserva, lo stesso tetto e
    lo stesso recupero dei job delle scene video; con `tetto_costo_usd` serve la durata
    della voce misurata;
  - `giv story run --scene <id> --new-job` abbandona un job partito che non si scarica più
    (addebito contato, job fra le riprese precedenti) e ne avvia uno nuovo.

### Modificato

- Con `--max-cost`, una configurazione ammessa dal modello ma senza tariffa nel listino
  (audio o risoluzione non indicati) rende la stima insufficiente: codice 2 col nome della
  configurazione e il flag da indicare. Per esempio `google/gemini-omni-1.2-flash` vuole
  `--audio on`. Senza `--max-cost` stima e piano non cambiano.
- `giv video` via OpenRouter rifiuta con codice 2, prima della chiamata, `--image` con
  `--ref` sui modelli `x-ai/…` (OpenRouter risponde 400) e propone `--provider xai`.
- `giv image` su OpenRouter non ripete più da solo la richiesta dopo un errore del server o
  un timeout e non segue redirect: l'immagine potrebbe essere stata generata e pagata
  (codice 5 con un suggerimento).
- I file generati non sovrascrivono mai un file che c'è già, né seguono un link al suo
  posto: il nuovo prende il suffisso `-2`, `-3`…
- Il motivo di un job fallito riporta solo codice e messaggio del provider, ripuliti, non
  più il campo `error` così com'è.

### Sicurezza

- I risultati indicati dal provider fuori dalla sua API si scaricano solo da `https` verso
  indirizzi pubblici (controllati sull'indirizzo effettivo), senza chiave né credenziali,
  coi redirect ricontrollati e senza `Referer`; video e immagini di xai sempre senza
  chiave. I messaggi d'errore dei provider sono filtrati: niente corpo grezzo, URL ridotti
  a schema e host.
- Avvii a pagamento e caricamenti di file non seguono redirect e non si ripetono da soli.
- Montaggio e scene avatar lavorano in una cartella privata e si fermano se `TMPDIR` o una
  cartella sopra è scrivibile da altri senza sticky bit; `giv story cut` tiene aperta la
  cartella dello storyboard fino alla pubblicazione del file finale, che compare solo
  completo.
- `giv story run`: se lo stato col costo reale del fotogramma chiave non si salva, la
  scena si ferma prima del video; il job lasciato con `--new-job` si controlla come quelli
  da recuperare.

## [0.9.1] - 2026-11-21

### Aggiunto

- **`giv story run --scene <id> --new-keyframe`**: rigenera a pagamento anche il
  fotogramma chiave della scena, con la stessa riserva e lo stesso tetto delle altre
  richieste; il fotogramma precedente resta nello stato fra le riprese precedenti
  (`previous`, `kind` `fotogramma`) e su disco. Vale solo con `--scene` e per una scena
  con `fotogramma_chiave`, altrimenti codice 2.
- **Aiuto di `giv story`**: `giv story help` (anche `--help`, `-h`) e
  `giv story check|run|cut --help`, su stderr e con codice 0.
- **Spesa di scene non più nello storyboard**: `giv story check` e `giv story run` la
  mostrano a parte (`removed_scenes` con `id` e `spent_usd`, `removed_spent_usd`, avviso
  su stderr e messaggio di rifiuto del tetto) e continua a contare nel tetto; `giv story
  check` la somma al costo previsto prima di confrontarlo con `tetto_costo_usd`.
- **Preset per Grok**: varianti `grok` di tutti i 18 preset di camera ed effetti per i
  modelli `grok-imagine-video-*` di xai, al posto della frase generica.

### Modificato

- Il fotogramma chiave registrato di una scena si riusa a ogni ripresa e **non si
  rigenera più da solo** quando il suo file manca: la scena esce con codice 2, senza
  richieste, e indica `--scene <id> --new-keyframe`. Con `--scene` un avviso ricorda il
  riuso.
- `tutela` nel blocco `default` e nelle scene dello storyboard è controllata: una
  clausola della libreria dei preset (`prodotto`, `illustrazione` o quelle dei file
  dell'utente) oppure `no`; un valore sconosciuto esce con codice 2 prima di qualsiasi
  generazione. Senza indicazione resta il default di `giv video`.
- Nel JSON di `giv story check` e `giv story run` il campo delle scene si chiama
  `scenes`, come in `giv story cut` (prima `scene`).
- `takes` nel JSON di `giv story run` conta solo le riprese video, non i fotogrammi
  chiave sostituiti.

## [0.9.0] - 2026-11-21

### Aggiunto

- **`giv job fetch --provider P <id>`**: riprende l'attesa di un job video già partito (per
  esempio dopo un'uscita con codice 5) e ne scarica il risultato **senza un nuovo avvio**;
  `--out`, `--name`, `--timeout` come `giv video`. Manifest e riga di registro come un
  video normale, con `command` `job fetch`. Ogni job avviato lascia il suo stato (id,
  indirizzo di attesa, modello, prompt inviato) in `giv-jobs.jsonl` nella directory
  corrente, creato con permessi 0600.
- **Storyboard** a più scene: un file JSON con `formato`, blocco `default` (provider,
  modello, risoluzione, aspect, audio, seed, tutela, parametri passanti),
  `tetto_costo_usd`, `strict`, insiemi di `riferimenti`, scene `video`, `clip` e
  `immagine` (`prompt`, `negative`, `preset`, `immagine`, `fotogramma_chiave`,
  `continua_da`, `ultimo_fotogramma`, `usa`, `costo_dichiarato_usd`, `voce`) e
  `montaggio` (transizione, musica, sottotitoli, uscita). Esempio in `examples/spot/`.
- **`giv story check <storyboard.json>`**: valida forma, file e riferimenti fra scene,
  controlla ogni scena video come `giv video --dry-run` sul catalogo del provider letto in
  quel momento (un modello sparito viene rifiutato) e somma stime da listino e costi
  dichiarati; le generazioni senza stima sono elencate a parte (`not_estimated`), mai
  contate come zero, e con `tetto_costo_usd` vengono rifiutate. Nessuna generazione. Il
  tipo di scena `avatar` viene rifiutato. Con `ffprobe`, avviso se la voce dura più della
  sua scena.
- **`giv story run [--scene <id>] <storyboard.json>`**: genera a pagamento le scene video
  in ordine, una richiesta alla volta (fotogramma chiave, poi clip; `continua_da` con
  l'ultimo fotogramma della scena prima, estratto con ffmpeg). Lo stato per scena
  (`fatto`, `partito`, `incerto`, `fallito`, `da_fare`) sta in `<nome>.stato.json`
  accanto allo storyboard: una nuova esecuzione riprende, recupera i job già partiti senza
  nuovo avvio e non rilancia da sola né le richieste rimaste senza esito né i job falliti
  sul provider; `--scene` rifà una scena tenendo la ripresa precedente.
  `tetto_costo_usd` è cumulativo sullo stato e si ricontrolla prima di ogni richiesta a
  pagamento, con la spesa fin qui più le stime da fare; cancellando lo stato il conto
  riparte. Clip in `clip/`, mai sovrascritte; JSON su stdout con stima e addebito
  affiancati, per scena e in totale.
- **`giv story cut <storyboard.json>`**: monta con ffmpeg in un solo video le scene già
  pronte (clip, immagini ferme e clip generate da `giv story run`, prese dal suo stato), con transizioni a
  taglio o dissolvenza, voce fuori campo per scena, musica in loop e sottotitoli incisi.
  L'uscita resta nella cartella dello storyboard e si riscrive a ogni montaggio; JSON su
  stdout con durata e formato del file scritto. Niente rete e nessuna generazione;
  servono `ffmpeg` e `ffprobe`.

### Modificato

- Avvio e attesa dei job video separati nel codice; il corpo della richiesta di avvio e il
  manifest di `giv video` non cambiano.
- `--timeout` copre anche i controlli dello stato e lo scaricamento del video, non solo
  l'attesa fra un controllo e l'altro.

### Sicurezza

- La chiave del provider va solo all'API del provider (schema, host e porta): nei
  redirect e negli scaricamenti fuori da quell'origine l'intestazione `Authorization`
  viene tolta, e un indirizzo di attesa fuori dall'API viene rifiutato. Il controllo dello
  stato di un job non segue redirect fuori dall'origine dell'API: è un errore e il job
  resta recuperabile.
- `giv job fetch` rifiuta con codice 2, senza richieste, un indirizzo di attesa salvato
  diverso dal percorso del job e un file di stato che non è un file regolare (anche un
  symlink).

## [0.8.0] - 2026-11-20

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
