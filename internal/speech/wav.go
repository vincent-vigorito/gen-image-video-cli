package speech

import (
	"encoding/binary"
	"errors"
	"math"
)

// WAVInfo sono le misure di un file WAV.
type WAVInfo struct {
	Format     int     `json:"format"` // 1 PCM, 3 float, 0xFFFE extensible
	Channels   int     `json:"channels"`
	SampleRate int     `json:"sample_rate"`
	Bits       int     `json:"bits"`
	DataBytes  int64   `json:"data_bytes"`
	DurationS  float64 `json:"duration_s"`
}

// ParseWAV legge l'intestazione di un WAV (RIFF/WAVE con i blocchi fmt e
// data) e ne calcola la durata. Tollerante sui canali e sui bit, che xAI non
// dichiara, e sulla lunghezza del blocco data scritta come sconosciuta
// (0 o 0xFFFFFFFF, come nei WAV generati in flusso): vale quello che c'è.
func ParseWAV(data []byte) (WAVInfo, error) {
	var w WAVInfo
	if len(data) < 12 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return w, errors.New("non è un file WAV")
	}
	var byteRate, blockAlign int
	haveFmt := false
	for p := 12; p+8 <= len(data); {
		id := string(data[p : p+4])
		size := int64(binary.LittleEndian.Uint32(data[p+4 : p+8]))
		body := int64(p + 8)
		rest := int64(len(data)) - body
		if id == "data" {
			if !haveFmt {
				return w, errors.New("WAV senza blocco fmt prima dei dati")
			}
			if size == 0 || size == 0xFFFFFFFF || size > rest {
				size = rest
			}
			w.DataBytes = size
			break
		}
		if size > rest {
			return w, errors.New("WAV troncato")
		}
		if id == "fmt " {
			if size < 16 {
				return w, errors.New("WAV: blocco fmt troppo corto")
			}
			f := data[body : body+size]
			w.Format = int(binary.LittleEndian.Uint16(f[0:2]))
			w.Channels = int(binary.LittleEndian.Uint16(f[2:4]))
			w.SampleRate = int(binary.LittleEndian.Uint32(f[4:8]))
			byteRate = int(binary.LittleEndian.Uint32(f[8:12]))
			blockAlign = int(binary.LittleEndian.Uint16(f[12:14]))
			w.Bits = int(binary.LittleEndian.Uint16(f[14:16]))
			haveFmt = true
		}
		p = int(body + size + size%2) // i blocchi sono allineati a 2 byte
	}
	if !haveFmt {
		return w, errors.New("WAV senza blocco fmt")
	}
	if w.Channels < 1 || w.SampleRate < 1 || w.Bits < 1 {
		return w, errors.New("WAV: canali, frequenza o bit non validi")
	}
	if w.Format != 1 && w.Format != 3 && w.Format != 0xFFFE {
		return w, errors.New("WAV non PCM: formato non supportato")
	}
	if byteRate <= 0 {
		byteRate = w.SampleRate * w.Channels * w.Bits / 8
		if blockAlign > 0 {
			byteRate = w.SampleRate * blockAlign
		}
	}
	if byteRate <= 0 {
		return w, errors.New("WAV: velocità dei dati non valida")
	}
	if w.DataBytes == 0 {
		return w, errors.New("WAV senza audio")
	}
	w.DurationS = math.Round(float64(w.DataBytes)/float64(byteRate)*1000) / 1000
	return w, nil
}

// PCM16WAV avvolge in un WAV i campioni PCM a 16 bit little-endian senza
// intestazione (audio/L16 di alcuni provider): un byte spaiato in coda si
// toglie.
func PCM16WAV(pcm []byte, rate, channels int) []byte {
	block := 2 * channels
	pcm = pcm[:len(pcm)-len(pcm)%block]
	out := make([]byte, 44, 44+len(pcm))
	copy(out[0:], "RIFF")
	binary.LittleEndian.PutUint32(out[4:], uint32(36+len(pcm)))
	copy(out[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(out[16:], 16)
	binary.LittleEndian.PutUint16(out[20:], 1)
	binary.LittleEndian.PutUint16(out[22:], uint16(channels))
	binary.LittleEndian.PutUint32(out[24:], uint32(rate))
	binary.LittleEndian.PutUint32(out[28:], uint32(rate*block))
	binary.LittleEndian.PutUint16(out[32:], uint16(block))
	binary.LittleEndian.PutUint16(out[34:], 16)
	copy(out[36:], "data")
	binary.LittleEndian.PutUint32(out[40:], uint32(len(pcm)))
	return append(out, pcm...)
}
