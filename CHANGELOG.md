# Changelog

Tutte le modifiche rilevanti del progetto, nel formato
[Keep a Changelog](https://keepachangelog.com/it-IT/1.1.0/);
versioning [SemVer](https://semver.org/lang/it/).

## [0.13.1] - 2026-11-23

> **Pulizia dei rilievi della 0.13.0, a costo zero.** `giv story run --max-cost` mette un
> tetto al singolo giro, in aggiunta a `tetto_costo_usd`; `--keyframes` elenca le scene da
> approvare; due fotogrammi chiave della stessa scena non collidono più sul nome;
> `"parallelo": 0` è un errore; nello stato l'addebito del giudizio dice se è la stima o
> il costo dai consumi. Nessuna chiamata a pagamento nello sviluppo e nelle prove di
> questa versione. **Senza `--max-cost` tutto si comporta come nella 0.13.0**; storyboard,
> kit e stati della 0.12.x e della 0.13.0 restano validi e i campi JSON nuovi si
> aggiungono.

### Aggiunto

- **Tetto del singolo giro: `giv story run --max-cost <usd>`.** Vale in aggiunta a
  `tetto_costo_usd` dello storyboard: si controllano entrambi, sul valore non arrotondato,
  e il giro si ferma al primo dei due (vince il più stretto). Il tetto del giro conta tutto
  ciò che il giro riserva e addebita: fotogrammi chiave, bozze, definitivi, voci,
  trascrizioni e giudizi; prima del primo avvio la stima del giro deve starci, poi prima di
  ogni richiesta a pagamento riserve e addebiti del giro più le stime ancora da fare. Oltre il tetto, o con una
  richiesta senza stima né costo dichiarato, codice 2 senza nuovi avvii; un valore non
  valido (non numerico, zero o negativo, non finito) è codice 2 prima di tutto. Vale per
  il definitivo, `--bozza` e `--keyframes`, anche con `--parallel`. Non si
  scrive né nello storyboard né nello stato: vale solo per il giro in cui lo si indica.
  Nel JSON `run_cap_usd` (il tetto del giro, solo con `--max-cost`) e `run_spent_usd`
  (con `--max-cost` presente anche a 0).

### Corretto

- **`--keyframes` con l'approvazione sul fotogramma chiave** (`"approvazione": true` senza
  `default.bozza`): `awaiting_approval` elenca le scene il cui fotogramma aspetta `giv
  story approve`.
- **Nomi dei file di `giv story run`**: due fotogrammi chiave della stessa scena generati
  nello stesso secondo non collidono più. Il nome resta
  `<id>-r<N>-fotogramma-<AAAAMMGG-hhmmss>.<ext>`; se il file c'è già, `-2`, `-3`… prima
  dell'estensione, come `giv image`. Vale per tutti i file di `giv story run` (clip, bozze,
  fotogrammi).
- **`"parallelo": 0`** scritto nel blocco `default` è un errore di validazione: codice 2
  da `giv story check` e `giv story run`, col messaggio dei valori ammessi (da 1 a 4).
  Il campo assente resta valido e vale 1.
- **Addebito del giudizio di aderenza**: nello stato l'addebito di tipo `giudizio` ha
  `cost_from_usage: true` quando `cost_est_usd` è il costo dai consumi riportati da Gemini;
  assente, è la stima (o un addebito scritto dalla 0.13.0); un valore che non sia `true` o
  `false` rende lo stato non valido. Totali, spesa e tetto non cambiano.

### Compatibilità

- Storyboard, kit e stati della 0.12.x e della 0.13.0 restano validi; senza `--max-cost`
  generazione, file, richieste, risultati e comandi restano quelli della 0.13.0.
  Un'eccezione voluta: uno storyboard con `"parallelo": 0` scritto, prima letto come 1, ora
  esce con codice 2 (togliere il campo o scrivere 1).
- Gli addebiti di giudizio degli stati della 0.13.0 non hanno `cost_from_usage`: si
  leggono come prima e non si riscrivono.

### Limiti noti

- Nessuna prova dal vivo in questa versione: tutto è stato provato con server finti e
  comandi locali.
- Veo non è risultato attivo sull'API Gemini diretta (nella 0.13.0 il definitivo è stato
  provato via OpenRouter).
- La bozza anticipa soggetto e inquadratura, non i difetti del definitivo.
- Alcune bozze arrivano con misure leggermente diverse da quelle richieste e con una
  traccia audio anche ad audio spento.

## [0.13.0] - 2026-11-23

> **Guardare e approvare una scena prima di pagarne il definitivo.** Prima i soli
> fotogrammi chiave (`giv story run --keyframes`), poi bozze video economiche (`--bozza`,
> col profilo `default.bozza`) montate con la scritta BOZZA (`giv story cut --bozza`); con
> `"approvazione": true` il definitivo parte solo per le scene approvate con `giv story
> approve`. Le scene possono restare aperte in parallelo (`--parallel`, fino a 4) dentro lo
> stesso tetto di spesa; il giudizio di aderenza con Gemini (`giv qc --judge`,
> `controllo.aderenza`) confronta quattro fotogrammi con la richiesta, solo su richiesta
> esplicita. **Senza le opzioni nuove tutto si comporta come nella 0.12.1**; storyboard,
> kit e stati della 0.12.x restano validi e i campi JSON nuovi si aggiungono.

### Aggiunto

- **Solo i fotogrammi chiave: `giv story run --keyframes`.** Genera a pagamento solo i
  `fotogramma_chiave` che mancano (quelli pronti si riusano), nessun video né voce, poi
  `foglio-contatto.jpg` e `foglio-approvazione.jpg`. Con `--scene <id> --new-keyframe`
  rifà una sola immagine: anche con un tetto conta solo il suo costo, stimato o dichiarato.
- **Bozze video: `default.bozza` e `giv story run --bozza`.** Nel blocco `default`
  `bozza` (`provider`, vuoto: quello del definitivo; `modello`, obbligatorio;
  `risoluzione`): le bozze hanno la stessa richiesta, durata e riferimenti del definitivo,
  file in `clip/bozza/` (`<id>-b<N>`…), stato (`draft` della scena) e spesa propri
  (addebiti di tipo `bozza`), con la stessa riserva e lo stesso tetto di ogni richiesta. Il
  fotogramma chiave è condiviso con il definitivo; `continua_da` parte dalla bozza della
  scena prima. Senza `default.bozza`, `--bozza` esce con codice 2.
- **Approvazione: `"approvazione": true` e `giv story approve`.** `giv story approve
  <storyboard> <scena>…` (o `--all`) registra nello stato, sotto lo stesso blocco di `giv
  story run`, il materiale visto (la bozza in uso, o il fotogramma chiave se non ci sono
  bozze) col suo `sha256` e la ricetta del definitivo (richiesta, modelli, durata,
  formato, riferimenti, file del kit col loro contenuto, `continua_da`). Locale e
  gratuito. Con le scene indicate è tutto o niente (codice 2, stato invariato); `--all`
  elenca in `skipped` le scene saltate col motivo. JSON: `material`, `approved` (`id`,
  `take`, `file`, `sha256`, `keyframe`, `already`), `skipped`; nello stato `approval`
  della scena. Con `approvazione: true` `giv story run` avvia il definitivo solo delle scene
  approvate con la ricetta di adesso; le altre (né fotogramma chiave né video) sono in
  `awaiting_approval` e su stderr col comando per approvarle. Una nuova bozza, un altro
  fotogramma chiave o un cambio della ricetta chiedono una nuova approvazione; un cambio in
  un'altra scena indipendente no. Le scene avatar non si approvano e partono come prima.
- **Montato delle bozze: `giv story cut --bozza`.** Monta le bozze in uso in
  `<nome>.bozza.mp4` (con `formati`, `<nome>.bozza-<WxH>.mp4`) con la scritta BOZZA su
  tutto il montato e riscrive solo `foglio-approvazione.jpg` (fotogramma chiave e bozza in
  uso di ogni scena video, con «approvata» nell'etichetta): montato definitivo, foglio di
  contatto e controllo restano come sono. Niente `--export`; nel JSON `draft: true`.
- **`giv story check` con bozze e approvazioni.** Per scena video `draft` (profilo, stato,
  file e stima della bozza) e `approval` (`da_preparare`, `da_approvare`, `pronta`,
  `in_corso`, `fatta`, motivo in `approval_note`); `run_status` `da_approvare` per i
  definitivi che aspettano l'approvazione, non contati in `run_cost_est_usd` (la spesa
  ancora prevista adesso); in cima `drafts_cost_est_usd`, `drafts_not_estimated` e
  `finals_cost_est_usd` (bozze e definitivi ancora da fare).
- **Scene in parallelo: `giv story run --parallel <n>` e `default.parallelo`.** Da 1 a 4,
  di partenza 1 (come prima); il flag prevale sul campo. Gli avvii restano uno alla volta
  e in ordine di storyboard, si sovrappongono solo attesa e scaricamento, e un solo
  coordinatore scrive lo stato. Prima di ogni avvio il tetto conta spesa registrata,
  riserve ancora aperte e stima della nuova richiesta, sul valore non arrotondato. Una
  scena con `continua_da` aspetta il file completo della precedente, nella stessa
  versione (bozza o definitivo). JSON: `parallel`.
- **Giudizio di aderenza: `giv qc <video> --judge`.** Quattro fotogrammi del video (JPEG,
  lato lungo al più 768 px, senza metadati), la richiesta della scena (`--prompt` o
  `--prompt-file`, al più 4000 caratteri) e il riferimento facoltativo `--ref` vanno in una
  sola richiesta a Gemini diretto (`gemini-3.5-flash-lite` di default, `--model`), **a
  pagamento** e **solo con `--judge`**: senza il flag nessun invio. Prima dell'invio su
  stderr destinatario, immagini, caratteri e stima; `--dry-run` mostra tutto senza
  inviare; `--max-cost` è il tetto (stima oltre il tetto o non stimabile: codice 2, nessun
  invio). Stima per eccesso dal listino (0,30 $ e 2,50 $ per milione di token d'ingresso e
  d'uscita; circa 0,008 $ a giudizio), addebito dai token riportati. Il rapporto
  `judgment` ha `verdict` (`aderente`, `non aderente`, `incerto`), `recognizable`,
  `action`, `reasons`, `defects`, `not_assessable`, `message`; il controllo `adherence` in
  `checks`; poi `judged_at`, `request`, `cost_est_usd`, `cost_source`, `cost_usage_usd`,
  `cost_usage_source`. Risposta illeggibile: `incerto`. Esito incerto (redirect, errore
  del server, rete): codice 5, riga `failed` in `giv-log.jsonl`, nessun rilancio.
- **Aderenza nello storyboard: `"controllo": {"aderenza": true}`**, spenta se assente.
  `giv story run` giudica a fine giro, con lo stesso servizio di `giv qc --judge`, la
  ripresa nuova di ogni scena video (il definitivo, o la bozza con `--bozza`; con
  `--keyframes` nessun giudizio; le avatar no), con la richiesta com'è andata al video.
  Stima nel piano e nel tetto (con tetto un giudizio non stimabile è codice 2 prima di ogni
  avvio), riserva prima dell'invio, addebito di tipo `giudizio`. Il giudizio resta legato
  alla ripresa (`judgements` nello stato) e si rilegge senza reinvio; uno incerto resta
  contato e non si rilancia. JSON: `scenes[].judgement`, `judgements`; nel controllo
  `adherence` sulla riga della scena. Nessun giudizio rigenera una scena.
- **Stato**: per scena `draft` (le bozze), `approval`, `charges` di ogni tipo (`clip`,
  `fotogramma`, `voce`, `trascrizione`, `bozza`, `giudizio`) con lo `status` della riserva
  (`aperta`, `incerta`), e `judgements` sulle riprese. Uno stato della 0.12.x si apre come
  prima e il conto della spesa coincide.
- **Esempio** `examples/spot/storyboard-approvazione.json`: bozze a 480p, tre scene in
  parallelo, `approvazione` e `controllo.aderenza`.

### Cosa è a pagamento

- A pagamento, sempre dentro il tetto: `giv story run` (voci, fotogrammi chiave, video),
  `--keyframes` (solo i fotogrammi chiave), `--bozza` (le bozze), la trascrizione di
  `controllo.voce`, il giudizio di `controllo.aderenza` e `giv qc --judge`.
- Gratuiti e senza rete: `giv story check` (legge solo il catalogo del provider), `giv
  story approve`, `giv story cut` e `giv story cut --bozza`, `giv qc` senza `--judge` né
  `--voice`.
- Il giudizio manda fotogrammi e richiesta a Gemini **solo su richiesta esplicita**; nessun
  aggiornamento lo attiva da solo.

### Compatibilità

- Storyboard, kit e stati della 0.12.x restano validi. Senza le opzioni nuove
  (`--keyframes`, `--bozza`, `--parallel`, `approvazione`, `default.bozza`,
  `default.parallelo`, `controllo.aderenza`) generazione seriale, file, richieste,
  risultati e comandi restano quelli della 0.12.1.
- Le riprese già fatte valgono come definitive, con la loro spesa e i job da recuperare;
  attivare `approvazione` governa solo gli avvii futuri. `giv story check` e `giv story
  cut` leggono i vecchi stati senza riscriverli.

### Corretto dopo le prove

- **Addebito del giudizio di aderenza**: nello stato e nella spesa del giro di `giv story
  run` il giudizio conta il costo dai consumi riportati da Gemini, non più la stima per
  eccesso.
- **`--bozza` e approvazione**: `awaiting_approval` elenca anche le scene la cui bozza
  aspetta l'approvazione.
- **`giv story check` col tetto**: il tetto si confronta come in `giv story run` (spesa già
  registrata nello stato più la stima del giro di adesso); l'intero storyboard oltre il
  tetto è solo un avviso (`storyboard_over_cap`). Stima dei giudizi di aderenza
  (`judge_cost_est_usd`) compresa in `run_cost_est_usd`; con tetto un giudizio non
  stimabile è codice 2.

### Verificato dal vivo

Prove del 23/11/2026, 3,10 $ in tutto.

- I nomi del kit mandati come parole da riconoscere correggono la trascrizione di
  «Swerpify» (xAI).
- `giv qc --judge` con `gemini-3.5-flash-lite` dà `aderente` sulla richiesta vera e `non
  aderente`, con il motivo giusto, su un dettaglio cambiato: circa 0,002 $ a giudizio dai
  consumi.
- Un fotogramma chiave dal kit (0,05 $).
- Tre bozze in parallelo con `alibaba/wan-3.0` via OpenRouter, aperte insieme: 0,48 $
  l'una addebitati contro 0,80 $ stimati.
- Montato delle bozze con la scritta BOZZA.
- Approvazione di una scena e definitivo solo per quella (Veo 3.1 via OpenRouter, 8 s,
  1,60 $), con le altre lasciate «da approvare».
- Giudizio sul definitivo, che ha segnalato a ragione la scritta del prodotto sparita.

### Limiti noti

- Veo non è risultato attivo sull'API Gemini diretta: il definitivo è stato provato via
  OpenRouter.
- La bozza anticipa soggetto e inquadratura, non i difetti del definitivo.
- Alcune bozze arrivano con misure leggermente diverse da quelle richieste e con una
  traccia audio anche ad audio spento.
- Tetto del singolo giro, scene da approvare dopo `--keyframes`, nomi dei fotogrammi
  chiave nello stesso secondo, `"parallelo": 0` e addebito del giudizio fra stima e
  consumi: chiusi nella 0.13.1.

## [0.12.1] - 2026-11-23

> **Pulizia dei rilievi più visibili della 0.12.0, a costo zero.** Il controllo tecnico non
> dà più avvisi a vuoto sul cartello finale e sulla copertina delle clip; `giv story check`
> dice cosa è già fatto e quanto costerebbe ciò che partirebbe adesso; i nomi propri vanno
> alla trascrizione come termini da riconoscere; `<nome>.controllo.json` dopo `giv story
> cut` riporta il confronto delle voci; `giv voice` con xAI ha il costo nel JSON. Nessuna
> chiamata a pagamento nello sviluppo e nelle prove di questa versione. I comandi, gli
> storyboard, i kit e gli stati della 0.12.0 danno lo stesso risultato; i campi JSON nuovi
> si aggiungono.

### Aggiunto

- **`giv story check` dice cosa è fatto e cosa partirebbe adesso**, dallo stato di `giv
  story run`: per scena `run_status`, lo stato di ogni parte (`fotogramma`, `video`,
  `voce`, `trascrizione`, solo quelle che la scena ha) fra `fatto`, `da_generare`,
  `partito` (job registrato, si recupera senza nuovo avvio), `incerto`, `fallito` e
  `mancante` (registrata ma file assente: non si rifà da sola), e `run_cost_est_usd`, la
  stima delle parti da generare; in cima `run_cost_est_usd` (ciò che `giv story run`
  lancerebbe adesso; senza stato è uguale a `cost_total_usd`) e `run_not_estimated`. Su
  stderr una riga per scena e il riepilogo «giv story run adesso: stima N $ (storyboard
  intero M $)». `cost_total_usd` resta il costo dell'intero storyboard.
- **Nomi propri per la trascrizione**: campo `nomi` del kit (facoltativo, al più 100 nomi
  da 1 a 50 caratteri, senza caratteri invisibili né ripetizioni a meno delle maiuscole;
  `giv kit check` li conta in `names`, e un kit con i soli nomi non è più vuoto) e `giv
  qc --voice --nome <nome>`, ripetibile, con gli stessi limiti (fuori: codice 2), che si
  aggiunge ai nomi del kit con `--kit`; nel `--dry-run` `params.nomi`. Il controllo della
  voce (`giv qc --voice` e `controllo.voce` di `giv story run`, che usa i nomi del kit)
  manda alla trascrizione xAI come termini da riconoscere (`keyterm`) prima i nomi, poi le
  parole scritte del dizionario delle pronunce, senza ripetizioni; oltre 100 un avviso e i
  seguenti non si inviano.
- **Confronto delle voci in `<nome>.controllo.json` dopo `giv story cut`** (e nelle
  esecuzioni di `giv story run` dopo quella che ha trascritto): le righe in `voices[]`
  delle voci già controllate portano `comparison` dall'esito registrato nello stato
  (`words`, `matched`, `ratio`, `different`, `missing`, `extra` con `expected`, `heard` e
  `position`, `truncated`), un riepilogo di al più 20 parole per elenco, ognuna di al più
  50 caratteri. Lo stato lo registra in `voice_check.comparison`, validato quando si legge;
  uno stato della 0.12.0 senza il campo resta valido.
- **Costo nel JSON di `giv voice` con xAI**: `cost_usage_usd` e `cost_usage_source`, gli
  stessi campi di Gemini. xAI non riporta il costo: è **calcolato da giv** sul listino (15
  $ per milione di caratteri) e sui caratteri del testo inviato, non un addebito letto dal
  provider; `cost_usage_source` lo dice.

### Corretto

- **Controllo tecnico, cartello finale**: nei montati dello storyboard (`giv qc
  <storyboard>` e il controllo di `giv story run` e `giv story cut`) gli ultimi secondi del
  cartello finale (`montaggio.cartello_finale.durata`, anche dal kit; 3 s se non indicata)
  non contano più fra i tratti fermi di `freeze`, e il `message` lo dice («esclusi gli
  ultimi N s (cartello finale)»). Un fermo prima del cartello, lungo almeno
  `--freeze-min`, resta un avviso. `giv qc <file>` non sa del cartello e non cambia.
- **Controllo tecnico, copertina**: una copertina allegata al file (traccia video
  `attached_pic`, come nelle clip di xAI) non dà più l'avviso «tracce in più» di
  `streams`, né in `giv qc` né nel controllo di `giv story run` e `giv story cut`; resta
  contata in `value.cover`. Una seconda traccia video o audio, sottotitoli e dati restano
  avvisi.

### Limiti noti

- **Nomi propri nella trascrizione, non verificato dal vivo**: che i nomi mandati come
  termini da riconoscere (`nomi` del kit, `--nome`) correggano davvero la trascrizione dei
  nomi propri (nella prova della 0.12.0 un nome di marca a inizio frase era stato
  trascritto in altro modo, due volte su due) non è provato: in questa versione nessuna
  trascrizione a pagamento, solo server finti. Una differenza su un nome resta un avviso
  da ascoltare prima di rifare la voce.

## [0.12.0] - 2026-11-22

> **Voce dentro giv, kit di marca e controllo dei file.** La voce fuori campo si genera con
> `giv voice` o dai testi dello storyboard (xAI predefinita, voce `eve`; Gemini seconda
> via), a pagamento: con xAI frazioni di centesimo per spot. Il kit (`kit.json`), i
> sottotitoli dai testi e il controllo tecnico (`giv qc`, anche dentro `giv story run` e
> `cut`) sono gratuiti e lavorano in locale; solo la trascrizione (`giv qc --voice`,
> `controllo.voce`) è a pagamento. I comandi e gli storyboard della 0.11.0 danno lo stesso
> risultato, salvo il picco audio, ora entro -1 dBTP.

### Aggiunto

- **`giv voice "<testo>"`** (xAI, `POST /v1/tts`, chiave `XAI_API_KEY`): voce fuori campo
  WAV (24 kHz) da un testo fra virgolette o da `--text-file` (UTF-8, al più 15.000
  caratteri), con i tempi per carattere in `<nome>.tempi.json`. Voce predefinita `eve`,
  scelta dopo l'ascolto dei campioni in italiano; lingua `it` (`--lang` BCP-47 o `auto`),
  `--speed` da 0,7 a 1,5, `--style` (solo gemini: xai lo ignora con un avviso),
  `--pronuncia <file.json>` (dizionario di al più 200 parole inviato a xAI, codice 2 fuori
  dai limiti prima di qualsiasi chiamata), `--voices` (elenco delle voci, gratuito),
  `--out`, `--name` (un file che c'è già non si sovrascrive: `-2`, `-3`…), `--dry-run`,
  `--max-cost`. Prezzo 15 $ per milione di caratteri del testo inviato (uno spot da 375
  caratteri costa circa 0,006 $): xAI non riporta il costo, il manifest ha la stima
  (`cost_est_usd`). Una sola richiesta, mai ripetuta: un esito incerto (redirect, errore
  del server, rete, risposta non valida) esce con codice 5 e una riga `failed` nel
  registro; voce sconosciuta codice 3.
- **Voce con Gemini**, seconda via (`giv voice --provider gemini`, chiave
  `GEMINI_API_KEY`, `POST /v1beta/interactions`): modelli `gemini-3.8-flash-tts`
  (predefinito) e `gemini-3.8-flash-lite-tts` (`--model`), voce predefinita `Kore`,
  `--style` a parole (al più 1000 caratteri), lingua rilevata dal testo (`--lang` non
  viene inviato), `--speed` ignorata con un avviso; al più 6000 caratteri detti, pronunce
  comprese. Gemini non ha dizionario né tempi: giv sostituisce le pronunce nel testo
  inviato, l'uscita è solo il WAV (24 kHz, niente `.tempi.json`) e i sottotitoli dai testi
  si distribuiscono sulla durata. Listino: 0,50 $ per milione di token di testo e 9 $
  (flash-lite 6 $) per milione di token audio, 25 al secondo; **dal 1° gennaio 2027 il
  doppio** (1 $ e 18 $, flash-lite 1 $ e 12 $). Dopo la generazione `cost_usage_usd` e
  `cost_usage_source` danno il costo dai token riportati o dalla durata. `giv voice
  --voices --provider gemini` elenca le voci, gratuito: sempre le 30 voci studio (compresa
  `Kore`) e poi quelle dell'API (`GET /v1beta/voices`) pagina per pagina, fino a 10 pagine
  e 10.000 voci; un elenco tagliato esce con codice 0, un avviso e `warnings` nel JSON.
- **`giv voice --kit <kit.json>`**: il blocco `voce` e le pronunce del kit fanno da
  predefiniti; i flag vincono e `--pronuncia` va sopra le pronunce del kit. Con un
  `--provider` diverso da quello del kit valgono solo lingua e pronunce, con un avviso.
- **Kit di marca e personaggi** (`kit.json`) e **`giv kit check <kit.json>`**: colori, logo,
  font, cartello finale, musica e volume della marca, personaggi con immagini di
  riferimento, descrizione e tutela, voce e pronunce, scritti una volta. Il kit è in sola
  lettura e chiuso nella sua cartella (percorsi relativi, anche attraverso i link, solo
  file normali, niente URL). `giv kit check` controlla forma e valori (campo sconosciuto,
  valore fuori dai limiti, file fuori dalla cartella o non valido: codice 2, un problema
  per riga) ed elenca i file con le misure, senza rete né spesa e senza scrivere niente;
  avvisa per logo senza trasparenza, font non leggibile, troppi riferimenti per i modelli
  video, descrizione lunga, `velocita` con Gemini. Esempio in `examples/kit/` (segnaposto
  e font libero DejaVu Sans Bold con la sua licenza).
- **Kit e voce dai testi nello storyboard** (`giv story check|run|cut`): campo `kit` (file
  relativo allo storyboard, anche fuori dalla sua cartella, mai URL) o `--kit <kit.json>`
  su `check`, `run` e `cut` (vince sul campo; vuoto: codice 2); il kit si usa solo se
  indicato. I valori si fondono campo per campo: vince la scena, poi lo storyboard, poi il
  kit. Logo, cartello finale, musica e stile dei testi del kit compaiono da soli nel
  montaggio (`colori.primario` è lo sfondo del cartello e il bordo dei testi,
  `colori.testo` il colore dei testi; `sotto_voce` solo se c'è una voce); ogni personaggio
  diventa un insieme di riferimenti da usare in `usa` (uno con lo stesso nome nello
  storyboard vince), con la descrizione davanti al prompt e la sua tutela. Campo `testo`
  della scena (al più 2000 byte) e blocco `parlato` (`provider`, `modello`, `voce`,
  `lingua`, `velocita`, `stile`, `pronuncia`; predefiniti dal kit, poi xai, `eve`, `it`):
  `giv story run` genera la voce **prima dei video** in `voce/<id>-v<N>.wav` (con
  `.tempi.json` dove il provider li dà), mai sovrascritta, e `giv story cut` la monta come
  la `voce` della scena. `--voices` genera solo le voci, senza toccare i video; `--scene
  <id> --new-voice` rifà a pagamento la voce di una scena. Ogni voce passa dalla stessa
  riserva e dallo stesso tetto delle altre richieste a pagamento (spesa registrata più le
  stime di voci e video ancora da fare, sul valore non arrotondato): con un tetto una voce
  non stimabile esce con codice 2 senza avvii; una voce dall'esito incerto resta contata e
  non si rilancia da sola (codice 2 finché `--scene <id> --new-voice`), un rifiuto
  immediato (4xx) toglie la riserva. Una voce fatta non si rifà da sola quando testo o
  impostazioni cambiano: `check` e `run` lo dicono. `giv story check` riporta per scena
  `text_chars`, `voice_status` (`da_generare`, `fatto`, `cambiata`, `incerto`),
  `voice_cost_est_usd` (con `voice_cost_probable` per gemini) e `voice_duration_est_s`, il
  totale `voice_cost_est_usd` e, col kit, `kit` con `file` e `inherited`; `giv story run`
  riporta `voices` e per scena `voice` e `voice_this_run`. Ogni voce entra in `giv log` con
  `command` `story voice`. Esempio in `examples/spot/storyboard-kit.json`, con sottotitoli
  dai testi e controllo dei file.
- **Sottotitoli dai testi** (`"montaggio": {"sottotitoli": "auto"}`): `giv story cut` li
  scrive dal `testo` di ogni scena con una voce (il file della scena o quella generata), in
  ogni formato, in basso dentro i margini sicuri, al più due righe, spezzati alle frasi e
  alla punteggiatura, con lo stile di `stile_testi`. I tempi sono quelli della voce
  (`<voce>.tempi.json` dello stesso testo, come lo scrivono `giv voice` e `giv story run`),
  col ritardo della voce; senza tempi (Gemini, o un file di voce senza `.tempi.json`) le
  righe si distribuiscono sulla durata della voce, con un avviso. Resta la grafia scritta: le pronunce servono solo
  ad allineare i tempi. Serve almeno una scena con `testo` (codice 2); un file di
  sottotitoli che si chiama proprio `auto` si scrive `./auto`.
- **`montaggio.senza`** (`["logo", "cartello", "musica"]`): toglie ciò che il kit mette da
  solo nel montaggio; uno storyboard che indica anche l'elemento tolto (per esempio
  `montaggio.logo` con `senza` `logo`) esce con codice 2.
- **Controllo dei file nello storyboard** (`"controllo": {"tecnico": true, "voce": true}`).
  Con `tecnico` `giv story run`, a fine giro, e `giv story cut`, dopo il montaggio,
  controllano come `giv qc` (soglie di default) la clip di ogni scena, ogni voce in uso e
  ogni montato: il risultato va in `<nome>.controllo.json` accanto allo storyboard
  (riscritto ogni volta, leggibile solo dall'utente, mai al posto di un file d'ingresso) e
  nel campo `controllo` del JSON, avvisi e difetti anche su stderr. Avvisa e basta: non
  rigenera, non scarta, non spende; con `strict` un difetto fa uscire `giv story run` con
  1, dopo il manifest. Senza ffmpeg o ffprobe il controllo è un avviso. Con `voce` `giv
  story run` trascrive ogni voce **generata da giv** (appena fatta, prima dei video, o già
  fatta e mai trascritta) e la confronta col testo come `giv qc --voice` (xai, lingua e
  pronunce del blocco `parlato`; serve `XAI_API_KEY`): **a pagamento** (0,10 $ l'ora, al
  minuto per eccesso), nella stima (`voice_check_cost_est_usd` in `giv story check`) e nel
  tetto, con la riserva prima della richiesta; se il tetto non basta la trascrizione si
  salta con un avviso (i video hanno la precedenza). Una sola richiesta, mai rilanciata:
  dall'esito incerto resta contata. Si carica solo `voce/<id>-v<N>.wav` della scena, file
  normale in una cartella `voce` vera. L'esito va in `voice_check` della scena nello stato
  e in `voices[]` del controllo; `giv story run` riporta `voice_checks`.
- **Spesa dell'esecuzione nel JSON di `giv story run`**: `run_cost_est_usd` e
  `run_spent_usd`, stime e spesa contata delle sole richieste partite in questa esecuzione
  (fotogrammi chiave, video, voci e trascrizioni; i job recuperati restano dell'esecuzione
  che li ha avviati), assenti se non è partito niente; `cost_est_usd`, `cost_usd` e
  `spent_usd` restano quelli dell'intero storyboard.
- **`giv qc <file|storyboard.json>`**: controllo tecnico in locale con `ffprobe` e
  `ffmpeg`, gratuito, senza scrivere file. Controlli `duration`, `size`, `fps`, `audio`,
  `black`, `freeze` (esclusi i tratti neri), `silence`, `loudness`, `peak` (picco vero
  sopra -1 dBTP avviso, sopra 0 difetto) e `streams`, ognuno `ok`, `avviso` o `difetto`
  coi tratti trovati; attese `--expect-duration`, `--expect-size`, `--expect-fps`,
  `--expect-audio`, `--expect-loudness`; soglie `--black-min`, `--freeze-min`,
  `--freeze-noise`, `--silence-min`, `--silence-noise`. Con `--for <canali>` il confronto
  col profilo di ogni canale di `giv export` (misure, fps, durata, codec, indice `moov` in
  testa, volume). Con uno storyboard una riga per scena (la clip in uso, con durata e
  audio chiesti) e una per montato di `giv story cut`; scene non generate e montati che
  mancano sono `assente`. Esce con 0 anche con difetti, con `--strict` un difetto esce
  con 1. Avvisa e basta: non corregge, non rigenera, non spende. I tratti si leggono
  mentre ffmpeg scrive: nessun evento si perde anche con un'analisi lunga.
- **`giv qc --voice <file audio> --script "<testo>"`** (o `--script-file`): dopo i
  controlli tecnici del file, la trascrizione con xAI (`POST /v1/stt`, chiave
  `XAI_API_KEY`, **a pagamento**: 0,10 $ per ora di audio) e il confronto parola per parola
  col testo atteso, senza maiuscole, punteggiatura e accenti; una parola detta come la sua
  pronuncia (`--pronuncia`, o le pronunce del kit con `--kit`) vale come quella scritta.
  File `wav`, `mp3`, `ogg`, `flac`, `aac`, `m4a` o `mp4`, al più 100 MiB, con una sola
  traccia audio (con più tracce codice 2 prima di ogni richiesta). Due controlli in coda a
  `checks`: `voice` (differenze: avviso; meno di metà delle parole attese sentite uguali:
  difetto) e `language` (lingua rilevata diversa da `--lang`, default `it`, o da quella del
  kit: avviso). Il JSON ha `mode` `voice`, `script`, `transcript` (`text`, `language`,
  `duration_s`, `words`), `comparison` (`words`, `matched`, `ratio`, `different`,
  `missing`, `extra`), `cost_est_usd` e `cost_usage_usd`. La stima viene dalla durata
  contata sui campioni decodificati, arrotondata al minuto per eccesso; `--max-cost`
  rifiuta con codice 2 se la supera o se la durata non si misura; `--dry-run` fa i
  controlli tecnici e stampa richiesta e stima senza trascrivere. Un esito incerto esce
  con codice 5 e una riga `failed` nel registro, senza rilancio.

### Modificato

- **Stato di `giv story run`** (`<nome>.stato.json`): per le voci dai testi scrive nella
  scena `voice`, `voice_key` (impronta di testo e impostazioni), `voice_status`,
  `voice_error` e, con `controllo.voce`, `voice_check`; addebiti e riprese precedenti di
  tipo `voce` e addebiti di tipo `trascrizione`; i file del kit non vi entrano. **Un giv più vecchio rifiuta uno stato scritto con le voci.** Gli storyboard e
  gli stati della 0.11.0 si leggono come prima.
- **Picco audio entro -1 dBTP** nei file di `giv post mix`, `giv export` e `giv story cut`:
  la normalizzazione chiede -1,5 dBTP e lascia il margine alla codifica AAC, che alzava il
  picco oltre -1 (sullo spot di prova della 0.11.0: da -0,8 a -1,3 dBTP, volume integrato
  -14,1 LUFS come prima). Il grafo dei filtri cambia solo nel parametro del picco.
- **Voce in ritardo** (`ritardo_voce` nello storyboard, `--voice-at` in `giv post mix`): il
  silenzio davanti alla voce si genera invece di tenerlo in memoria con `adelay`, così la
  memoria non cresce col ritardo con nessuna versione di ffmpeg (con ffmpeg 7.1 non
  cresceva già); la voce parte allo stesso campione di prima. Senza ritardo il grafo non
  cambia.

### Sicurezza

- Il file `.ass` di lavoro convertito da un `.srt` (sottotitoli di `giv story cut` nei
  margini sicuri) torna a 0600 prima di essere riscritto: prima restava coi permessi con
  cui l'aveva creato ffmpeg.

### Limiti noti

- **Voce su xAI**: xAI conserva richieste e risposte per 30 giorni: solo testi di
  marketing, mai dati di clienti. I tempi per carattere sono quelli di ciò che la voce ha
  detto (col dizionario, la pronuncia): giv li riallinea al testo scritto; se mancano o
  non tornano, solo il WAV, con un avviso.
- **Voce con Gemini senza un costo massimo garantito**: ogni richiesta porta il limite di
  16.384 token in uscita e la stima (8192 token in ingresso più il limite in uscita:
  0,15 $ a richiesta con flash, 0,10 $ con flash-lite, il doppio dal 2027) è *probabile*,
  non un massimo. Finché una prova reale non conferma che l'API rispetta il limite, con un
  tetto attivo (`--max-cost` o `tetto_costo_usd`) la voce Gemini non è stimabile: codice 2
  prima di qualsiasi richiesta («usa xai o togli il tetto»), e `giv story check` e `giv
  story run` rifiutano allo stesso modo uno storyboard con `tetto_costo_usd` e una voce
  Gemini ancora da generare. Senza tetto parte, con un avviso su stderr. La voce xAI (prezzo per carattere, stima esatta) non cambia.
- **Durata di una voce non ancora generata**: `giv story check` la stima dai caratteri
  (circa 15 al secondo) e l'avviso lo dice; per le durate vere `giv story run --voices`
  (centesimi), poi di nuovo `check`. Un kit cambiato dopo la generazione non rigenera
  niente: cambia solo il montaggio e le generazioni successive.
- **`giv qc --voice`**: la trascrizione sbaglia sui nomi propri, quindi una differenza è
  un avviso da ascoltare, non un errore. La granularità dell'addebito della trascrizione
  non è documentata: la stima conta i minuti interi (30 secondi stimati 0,001667 $, il vero
  è circa la metà).
- **`giv qc`, scene quasi ferme**: con la soglia predefinita (`--freeze-noise -60`) una
  ripresa quasi ferma ma non immobile può non risultare ferma; -50 la trova più spesso, a
  costo di qualche falso allarme. Soglie da tarare sulle clip vere.
- **Solo il fotogramma chiave non si rigenera**: `giv story run --scene <id>
  --new-keyframe` rifà anche la clip, e con un tetto la stima di entrambi deve starci.
- **`giv image` senza stima né tetto**: non ha `--dry-run` né `--max-cost`; il costo si
  vede solo dopo la chiamata (`cost_usd` dove il provider lo riporta).

### Prove dal vivo

Spesa complessiva delle prove della 0.12.0: circa 0,08 $ (28 richieste), solo materiale
nostro.

- **Voce xAI**: 12 chiamate su 12 riuscite, WAV mono a 24 kHz con i tempi per carattere;
  la voce `eve` è stata scelta all'ascolto.
- **Voce Gemini**: provata su due frasi brevi, funziona e il limite dei token in uscita è
  stato rispettato in quei due casi, che non bastano a dire che l'API lo rispetti sempre:
  il tetto resta non stimabile (vedi sopra).
- **`giv story run --voices`** sullo spot di prova col kit: 6 voci `eve`, nessun video
  rigenerato, poi montato con logo, cartello finale e musica dal kit.
- **Kit e fotogramma**: il fotogramma del personaggio coi riferimenti del kit è stato fatto
  con `giv image`; il passaggio dal kit al fotogramma chiave dentro `giv story run` è
  coperto solo dai test con un server finto.
- **Prova finale sullo spot di prova col kit** (`controllo` tecnico e voce, `sottotitoli`
  `"auto"`, quattro formati): `giv story run`, `giv story cut --export
  instagram,tiktok,youtube,linkedin` e `giv qc` escono con 0; nessun video, fotogramma o
  voce nuova (`run_cost_est_usd` e `run_spent_usd` 0,01 $: solo le trascrizioni). 4 montati
  e 4 export da 30,3 s con sottotitoli dai testi, logo, cartello finale e musica dal kit,
  -13,9 LUFS, picco vero -1,8 dBTP (-1,5 negli export a 1080x1920). I sottotitoli ci sono in
  tutti i formati, spezzati in righe, e non toccano i titoli in alto. Controllo: 113
  controlli a posto, 11 avvisi, 0 difetti.
- **Trascrizione**: 6 voci trascritte da `controllo.voce` in `giv story run` e una con `giv
  qc --voice`, 0,0117 $ in tutto; 5 voci su 6 uguali al testo, la sesta con un avviso (il
  nome della marca a inizio frase, vedi i limiti noti).

## [0.11.0] - 2026-11-22

> **Tutto in locale**: i comandi nuovi lavorano con `ffmpeg` e `ffprobe` sui file che ci
> sono già, senza rete, chiavi né spesa. **Convenzioni, non valori ufficiali**: il volume
> finale di -14 LUFS di `giv post mix` (nessuna fonte ufficiale dei canali: è
> impostabile con `--loudness`) e i margini sicuri dei formati diversi dal 9:16 (5% per
> lato); ufficiali solo i margini del 9:16 (guida annunci Meta per i Reels: 14% in alto,
> 35% in basso, 6% ai lati).

### Aggiunto

- **`giv post <operazione>`**: rifinitura di un video in locale, gratuita. Flag comuni
  `--video`, `--out`, `--name`, `--dry-run`; l'ingresso non si sovrascrive mai (un nome
  preso dà `-2`, `-3`…, un'uscita uguale all'ingresso esce con codice 2), il file compare
  a lavoro riuscito; JSON su stdout con `command` (`post trim`…), `provider` `local`,
  `model` `ffmpeg`, `inputs`, `params`, misure del file e `cost_usd` 0, e una riga a costo
  zero in `giv-log.jsonl`; valori non validi con codice 2 prima di lanciare ffmpeg.
  - `trim` (`--start`, `--end`), `speed` (`--factor` da 0,25 a 4, audio alla stessa
    velocità con `atempo`), `freeze` (`--at`, `--duration` fino a 60 s, audio in pausa).
  - `reframe --aspect 9:16|1:1|4:5|16:9 [--fit crop|pad|blur] [--focus <x>[,<y>]]`: il lato
    corto resta quello del video; `--focus` sceglie il punto da tenere nel ritaglio
    (inquadratura fissa).
  - `overlay`: logo (`--logo`, `--logo-pos`, `--logo-width`, `--logo-opacity`,
    `--logo-from`, `--logo-to`), titolo (`--title`, `--from`, `--duration`, `--pos`) e
    cartello finale accodato (`--end-card`, `--end-color`, `--end-logo`, `--end-text`,
    `--end-subtext`, `--end-duration`), con `--font` e `--color`; tutto dentro i margini
    sicuri; testi resi alla lettera in un file `.ass` generato, mai nei filtri di ffmpeg.
  - `mix`: musica (`--music`, `--music-volume`, ripetuta o tagliata sulla durata del
    video), `--duck` che la abbassa sotto la voce, voce fuori campo (`--voice`,
    `--voice-at`), volume finale in due passate (`--loudness`, default -14 LUFS, `off`
    per lasciarlo com'è).
- **`giv sheet <file|cartella|storyboard.json>`**: foglio di contatto JPEG in locale
  (`--every <s>` oppure `--frames <n>`, `--cols`, `--labels`); con una cartella una riga
  per file, con uno storyboard una riga per scena col fotogramma chiave e la clip in uso.
- **`giv frame --video <file> --at <s>|--last [--width <px>]`**: un fotogramma PNG o la
  sua miniatura, in locale.
- **`giv gif --video <file> [--start] [--end] [--width] [--fps]`**: GIF animata in locale
  con la palette calcolata in due passate (default 480 px, 12 fps, al più 60 s).
- **Storyboard, campi nuovi del montaggio** (tutti facoltativi: gli storyboard di prima
  danno lo stesso video): `montaggio.formati` (un file per formato, `<uscita>-9x16.mp4`…,
  nessun formato predefinito), `adatta` (`bande`, `ritaglio`, `sfocato`), `logo`,
  `titoli` (tempi relativi a una scena), `cartello_finale`, `stile_testi`, `volume` (LUFS),
  `musica.sotto_voce` e `musica.sfuma_fine`; per scena `fuoco`, `taglio` e `velocita`;
  `foglio_contatto`. `giv story cut --formato <r>` monta un solo formato, `--export
  <canali>` esporta per canale dal montato. Con i formati un `.srt` sta nei margini sicuri
  del formato; nei verticali e nell'1:1 un titolo in alto col logo in alto dà un avviso;
  nel 9:16 il sottotesto del cartello va subito sotto il testo (anche in `giv post
  overlay`).
  `giv story run` e `giv story cut` riscrivono `foglio-contatto.jpg` accanto allo
  storyboard. `examples/spot` usa i campi nuovi (con un
  segnaposto in `musica/`).
- **`giv export --video <file> --for <canali>`**: un file per canale (`instagram`,
  `tiktok`, `youtube`, `linkedin`, ognuno con `:<formato>` facoltativo; predefiniti 9:16
  per Instagram Reel e TikTok, 16:9 per YouTube e LinkedIn) col profilo del canale
  (misure, fps, H.264 High, AAC 48 kHz, volume a -14 LUFS con `--loudness`, `--fit` e
  `--focus` come `reframe`); una durata fuori dai limiti del canale esce con codice 2
  prima di scrivere. Le convenzioni non ufficiali (volume, misure del feed di Instagram,
  formato di TikTok) sono elencate nel manifest in `exports[].convention`.

### Limiti noti

- **Picco del montato**: il volume finale di `giv story cut` e `giv post mix` arriva al
  valore chiesto (-14 LUFS), ma il picco reale (true peak) può restare a -0,8 dBTP invece
  di -1; le esportazioni di `giv export` stanno a -1,0/-0,9 dBTP. Da rivedere.
- **Titoli e logo**: nessuno spostamento automatico; un titolo in alto col logo in alto
  dà solo un avviso, la posizione la sceglie l'autore.
- **Sottotitoli `.srt` nel 9:16**: coi formati stanno sopra il 35% in basso (margine
  sicuro dei Reels), quindi a metà quadro; per un'altra posizione serve un `.ass` col
  suo stile.

## [0.10.0] - 2026-11-22

> **Non ancora provati dal vivo**: il provider fal (`giv avatar`, Kling 3 Pro, Kling 4.0 con
> `--keyframe` e `--shot`, Seedance 2.0 da riferimenti, `giv upscale --provider fal`) e il
> video con Gemini diretto (`giv video`, `giv extend` e `giv edit` con `--provider gemini`)
> sono implementati e provati solo contro server finti. Provati dal vivo il 22/11/2026:
> `giv extend` e `giv edit` con xAI.

### Aggiunto

- **`giv extend` e `giv edit`** (xAI, `grok-imagine-video`): allungano una clip di 2-10 s
  (`--duration`, default 6) o la modificano col prompt. `--video` è un file mp4 locale
  (al massimo 50 MiB, caricato su xAI con scadenza di un'ora) o un URL `https` che finisce
  in `.mp4`. Limiti del sorgente (2-15 s per extend, fino a 8,7 s per edit) controllati
  con ffprobe; il manifest riporta il costo reale di xAI (`cost_usd`) con la stima
  accanto. La stima segue la fatturazione vista nella prova del 22/11/2026: per extend
  secondi aggiunti × 0,07 $/s più il sorgente (a secondi interi) × 0,01 $/s, 0,27 $ per
  3 s aggiunti a una clip di 6 s; per edit sorgente × 0,08 $/s, 0,48 $ sulla stessa
  clip.
- **Video con Gemini diretto** (`gemini-omni-1.2-flash`, API interactions): `giv video
  --provider gemini` torna a generare, a pagamento, solo con una scelta esplicita del
  provider (`--provider gemini`, provider dello storyboard o `GIV_PROVIDER`). Audio sempre
  presente (`--audio off` rifiutato), 3-15 s (default 10), 360p-4k (default 720p), 16:9 o
  9:16, primo e ultimo fotogramma (con l'ultimo al massimo 10 s), fino a 6 `--ref`; stima
  massima a token e, a fine job, `cost_usage_usd` dai token riportati; `giv job fetch
  --provider gemini`.
- **`giv extend` e `giv edit` con Gemini** (`--provider gemini --from <job_id>`): dall'UE
  Gemini estende e modifica solo i video che ha generato, quindi `--from` è l'id di un
  video gemini fatto con giv (`--video` rifiutato); extend aggiunge 3-10 s fino a 30 s,
  edit fino a 15 s; misure del video di partenza da `giv-log.jsonl`.
- **Kling 4.0 su fal** (`kling-4.0`): `--image` obbligatorio, 3-30 s, 720p, 1080p
  (default) o 4k, fino a 8 fotogrammi intermedi con `--keyframe`, 0,157-0,549 $/s.
- **`--shot "<s>:<testo>"`** (fal `kling-4.0` e `kling-v3-pro`): inquadrature ripetibili, il
  testo sta tutto lì (niente prompt né `--preset`) e il video dura la somma. `--keyframe` e
  `--shot` con altri modelli escono con codice 2.
- **`giv upscale`** (OpenRouter, `black-forest-labs/flux-video-upscale`): `--factor` 1,5-3
  (default 2), `--mode precise|creative` (default `precise`), prompt facoltativo. Solo da
  URL `https` pubblico: un file locale esce con codice 2 e il messaggio propone
  `--provider fal` (nella prova del 22/11/2026 OpenRouter ha rifiutato il file mandato come
  data URI, HTTP 400, senza addebito). Da URL il sorgente non si misura: niente stima e,
  con `--max-cost`, codice 2; il costo è il `cost_usd` di OpenRouter.
- **`giv upscale --provider fal`** (Topaz, `topaz-upscale`): `--factor` 1-4 (default 2), file
  mp4 locale caricato sullo storage di fal o URL `https`; stima per secondo del sorgente e
  fascia d'uscita (0,01, 0,02 o 0,08 $/s, il doppio sopra i 30 fps) sulle misure di
  ffprobe, fps compresi.
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
    (addebito contato, job fra le riprese precedenti) e ne avvia uno nuovo;
  - `fotogrammi_intermedi` e `inquadrature` sulle scene video, come `--keyframe` e
    `--shot`; i fotogrammi intermedi da URL si controllano prima del fotogramma chiave a
    pagamento.

### Modificato

- `giv video` e `giv story` senza `--provider`, senza provider nello storyboard e senza
  `GIV_PROVIDER` escono con codice 2 come nella 0.9.1, ma il messaggio elenca anche gemini
  e fal: il default `gemini` vale solo per le immagini, un comando vecchio non comincia a
  spendere.
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
- `giv log --sum-cost`: `cost_est_usd` somma solo le righe senza costo riportato (nei
  comandi sulle clip la stima sta accanto a `cost_usd` e non si conta due volte).

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
- Gemini video: la chiave va solo all'origine dell'API, la richiesta che genera non segue
  redirect e non si ripete, il video fuori dall'API si scarica solo da `https` verso
  indirizzi pubblici e senza chiave, i testi d'errore sono filtrati.
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
