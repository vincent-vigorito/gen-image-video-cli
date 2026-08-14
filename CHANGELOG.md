# Changelog

Tutte le modifiche rilevanti del progetto, nel formato
[Keep a Changelog](https://keepachangelog.com/it-IT/1.1.0/);
versioning [SemVer](https://semver.org/lang/it/).

## [Unreleased]

### Aggiunto

- Skill agent-native `skills/gen-image-video/SKILL.md`: contratto di output,
  matrice provider verificata, ricette (reference di stile, image-to-video),
  costi indicativi e quirk noti. Installabile con un symlink in `~/.claude/skills/`.

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
