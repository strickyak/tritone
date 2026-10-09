# tritone

`tritone` is a Go program that analyzes an audio file, divides it into short chunks (e.g. 20ms) with fade-in and fade-out on the edges, computes the frequency spectrum of each chunk using FFT ([`github.com/mjibson/go-dsp/fft`](https://github.com/mjibson/go-dsp/fft)), and identifies $N$ (default $N=3$) strong peak frequencies representing each chunk along with their relative strengths. The output resynthesizes the audio using continuous tones with edge fade-in and fade-out.

## Features

- **Automatic Format Conversion**: If the input filename does not end in `.raw` or `.wav`, `ffmpeg` is automatically invoked to convert it to a temporary `.wav` file.
- **Direct Playback with MPlayer**: If the output file is not specified, `tritone` writes the synthesized audio to a temporary `.wav` file and plays it directly using `mplayer`.
- **Chunking & Windowing**: Divides audio into short chunks (default 20ms = 960 samples at 48kHz) and applies a cosine-tapered edge fade (fade-in and fade-out) before FFT analysis to eliminate edge discontinuity and spectral leakage.
- **FFT Spectral Analysis**: Computes forward FFT using [`github.com/mjibson/go-dsp/fft`](https://github.com/mjibson/go-dsp/fft) with zero-padding for high frequency resolution.
- **Peak Detection & Sub-bin Refinement**:
  - Detects local spectral maxima.
  - Refines peak frequencies with sub-bin parabolic interpolation.
  - Enforces minimum frequency separation between peaks (default 70 Hz) so sidelobes or nearby frequencies are not duplicated.
  - Computes relative strengths ($RelPower_i$) among the selected peaks.
- **Voice Continuity & Melodic Tracking**:
  - Tracks $N$ active voices across consecutive chunks.
  - Solves optimal voice-to-peak assignment to preserve pitch trajectories and minimize voice jumping.
  - Preserves phase continuity ($\theta$) across chunk boundaries, avoiding harsh phase clicks.
  - Linearly interpolates frequency and amplitude across chunks for smooth transitions.
- **Output Synthesis & Edge Fading**:
  - Synthesizes tone combinations matching input dynamics and relative strengths.
  - Applies fade-in and fade-out envelopes to each output chunk.
  - Supports both non-overlapping sequential chunks and overlap-add crossfading.
- **Audio Formats Supported**:
  - **Mono Unsigned 16-bit Integer PCM** (`u16le`): [0, 65535] with 32768 zero-level at 48000 samples/sec.
  - **Standard WAV**: 16-bit signed PCM, 8-bit unsigned PCM, 24-bit PCM, 32-bit float PCM; mono or stereo (automatically downmixed to mono).
  - **Signed 16-bit Integer PCM** (`s16le`).
  - **Media files via ffmpeg**: Any format supported by ffmpeg (`.mp3`, `.ogg`, `.flac`, `.m4a`, etc.).
  - **Standard input / output streaming**: Pipe audio through `stdin` and `stdout`.

---

## Installation & Building

```bash
cd tritone
go build -o tritone .
```

---

## Usage

```bash
# Play any sound file (mp3, ogg, etc.) directly using mplayer (output unspecified)
./tritone song.mp3

# Play a wav or raw file directly
./tritone sound.wav
./tritone audio.raw

# Convert and save output to a file
./tritone song.mp3 output.wav
./tritone input.raw output.wav
./tritone input.wav output.raw

# Process raw Mono Unsigned 16-bit audio at 48000 Hz
./tritone -in-format u16le -out-format u16le -rate 48000 input.raw output.raw

# Pipe audio via stdin and stdout
cat input.raw | ./tritone --quiet - - > output.raw

# Print detailed per-chunk frequency peaks and relative strengths
./tritone -v song.mp3 output.wav
```

### Options

| Flag | Default | Description |
|------|---------|-------------|
| `-i`, `-input` | `stdin` | Path to input sound file (or `-` for stdin) |
| `-o`, `-output` | *(unspecified -> mplayer)* | Path to output sound file (or `-` for stdout; plays with `mplayer` if omitted) |
| `-n`, `-tones` | `3` | Number of strong peak frequencies per chunk ($N$) |
| `-chunk-ms` | `20.0` | Chunk duration in milliseconds |
| `-fade-ms` | `2.0` | Fade-in / fade-out duration on chunk edges in ms |
| `-min-dist` | `70.0` | Minimum frequency distance between peaks in Hz |
| `-min-freq` | `50.0` | Minimum frequency to analyze in Hz |
| `-max-freq` | `12000.0`| Maximum frequency to analyze in Hz |
| `-rate` | `48000` | Sample rate in Hz for raw PCM audio |
| `-in-format` | `auto` | Input format: `auto`, `wav`, `u16le`, `s16le` |
| `-out-format`| `auto` | Output format: `auto`, `wav`, `u16le`, `s16le` |
| `-overlap` | `0.0` | Overlap fraction between chunks (0.0 to 0.75) |
| `-smooth` | `true` | Enable continuous smooth voice transitions |
| `-v`, `-verbose` | `false`| Print detailed per-chunk frequency analysis |
| `-quiet` | `false` | Suppress non-error progress messages |

---

## Testing

Run the test suite:

```bash
go test -v ./...
```

## Prompt (Gemini 3.8 Flash · high)

Write a program in Go called `tritone` that inputs a sound file, divides
it into short chunks (say maybe 20ms) and maybe with a little fade-in and
fade-out on the edges, performs FFT to get its frequency spectrum for each
short piece, and picks N (initially, N=3) strong peak frequencies (not
too close to each other) to represent that chunk, noting their relative
strengths, and aiming for some continuity.   The output is composed of
playing those tones for each chunk, with a little fade-in and fade-out
on the edges.  Support at least Mono Unsigned 16-bit integer audio,
48000 samples per second.   Prefer github.com/mjibson/go-dsp/fft for
the FFT code.

If the input filename does not end with suffux .raw or .wav, use the
`ffmpeg` command to try to convert it to a temporary .wav .  If the
output is not specified, play it from a temporary .wav file with
`mplayer` command.
