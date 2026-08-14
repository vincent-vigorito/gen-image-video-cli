# Changelog

Tutte le modifiche rilevanti del progetto, nel formato
[Keep a Changelog](https://keepachangelog.com/it-IT/1.1.0/);
versioning [SemVer](https://semver.org/lang/it/).

## [Unreleased]

### Corretto

- OpenRouter: fallback automatico a `modalities: ["image"]` per i modelli
  image-only (es. `bytedance-seed/seedream-5-0-pro`), che rifiutano la
  richiesta standard `["image", "text"]` con un 404 sulle modality.

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
