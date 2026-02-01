# Adding Voiceover to Asciinema Recording

GIFs don't support audio, so you need to convert to **video (MP4)** first, then add voiceover.

---

## Workflow Overview

1. **Convert asciinema → MP4** (using agg or ffmpeg)
2. **Record voiceover** (or use text-to-speech)
3. **Sync video + audio** (using ffmpeg or video editor)
4. **Export final video**

---

## Step 1: Convert Asciinema to MP4

### Option A: Using `agg` (Renders as frames, then convert)

**First, render to high-FPS GIF:**
```bash
agg --fps-cap 30 loomdemo loomdemo-hq.gif
```

**Then convert GIF to MP4 with ffmpeg:**
```bash
brew install ffmpeg

ffmpeg -i loomdemo-hq.gif -movflags faststart -pix_fmt yuv420p \
  -vf "scale=trunc(iw/2)*2:trunc(ih/2)*2" \
  loomdemo-silent.mp4
```

### Option B: Using `asciinema-automation` (Direct to video)

```bash
npm install -g @asciinema/asciicast2gif

# Convert to video-ready format
asciicast2gif --speed 1.3 --theme monokai loomdemo loomdemo-frames
```

### Option C: Using `terminalizer` (If you want to re-record)

```bash
npm install -g terminalizer

# Record a new session with terminalizer (if needed)
terminalizer record demo

# Render to GIF/MP4
terminalizer render demo --format mp4 -o loomdemo.mp4
```

---

## Step 2: Record Voiceover

### Option A: Record Your Own Voice

**Using QuickTime (macOS built-in):**
1. Open QuickTime Player
2. File → New Audio Recording
3. Click record, read your narration script
4. File → Save as `loomdemo-voiceover.m4a`

**Using Audacity (free, more control):**
```bash
brew install --cask audacity
```
1. Open Audacity
2. Click red record button
3. Read narration from `loomdemo-narration.md`
4. Export → Export as MP3/WAV: `loomdemo-voiceover.mp3`

### Option B: Text-to-Speech (Automated)

**Using macOS `say` command:**
```bash
# Extract narration text (remove markdown formatting)
say -f loomdemo-narration.md -o loomdemo-voiceover.aiff

# Convert to MP3
ffmpeg -i loomdemo-voiceover.aiff loomdemo-voiceover.mp3
```

**Using ElevenLabs / OpenAI TTS (better quality):**
```bash
# Use OpenAI TTS API (requires API key)
curl https://api.openai.com/v1/audio/speech \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "tts-1-hd",
    "input": "Your narration text here...",
    "voice": "onyx"
  }' \
  --output loomdemo-voiceover.mp3
```

---

## Step 3: Sync Video + Audio

### Option A: Simple Overlay (Audio plays from start)

```bash
ffmpeg -i loomdemo-silent.mp4 -i loomdemo-voiceover.mp3 \
  -c:v copy -c:a aac -map 0:v:0 -map 1:a:0 \
  -shortest \
  loomdemo-final.mp4
```

**Flags:**
- `-c:v copy` - Don't re-encode video (faster)
- `-c:a aac` - Encode audio as AAC
- `-shortest` - Stop when shortest stream ends (video or audio)

### Option B: Advanced Sync with Timing

If your voiceover has specific timing cues (e.g., "at 2:30, say this"):

```bash
# Add audio starting at specific timestamp
ffmpeg -i loomdemo-silent.mp4 -i loomdemo-voiceover.mp3 \
  -filter_complex "[1:a]adelay=10000|10000[delayed]; \
                   [0:a][delayed]amix=inputs=2[aout]" \
  -map 0:v -map "[aout]" -c:v copy -c:a aac \
  loomdemo-final.mp4
```
- `adelay=10000|10000` - Delay audio by 10 seconds (10000ms)

### Option C: Use a Video Editor (GUI, more control)

**iMovie (macOS built-in):**
1. Open iMovie
2. Create new project
3. Import `loomdemo-silent.mp4`
4. Import `loomdemo-voiceover.mp3`
5. Drag both to timeline
6. Align audio with video beats (use narration script timestamps)
7. File → Share → File → Export

**DaVinci Resolve (free, professional):**
```bash
# Download from: https://www.blackmagicdesign.com/products/davinciresolve
```
1. Import video and audio
2. Place on timeline
3. Use narration script to sync key moments
4. Export as MP4

---

## Step 4: Add Chapters/Markers (Optional)

For a 6+ minute video, add chapter markers:

```bash
# Create chapters.txt
cat > chapters.txt << 'EOF'
00:00:00 Introduction
00:00:30 Act 1: Discovering Weaver
00:01:45 Act 2: Weaver in Action
00:02:30 Act 3: Table Discovery
00:04:00 Act 4: Geospatial Analysis
00:06:00 Closing
EOF

# Add chapters to MP4
ffmpeg -i loomdemo-final.mp4 -i chapters.txt -map 0 -map_metadata 1 \
  -codec copy loomdemo-with-chapters.mp4
```

---

## Recommended Workflow for loomdemo

Based on your 6:28 demo and the narration script:

### Quick & Easy (Automated TTS)

```bash
# 1. Convert to MP4
agg --fps-cap 30 loomdemo loomdemo-hq.gif
ffmpeg -i loomdemo-hq.gif -movflags faststart -pix_fmt yuv420p \
  -vf "scale=trunc(iw/2)*2:trunc(ih/2)*2" loomdemo-silent.mp4

# 2. Extract narration (remove markdown)
grep -v '^#' loomdemo-narration.md | \
  grep -v '^\*\*' | \
  grep -v '^```' | \
  grep -v '^\-\-\-' > narration-clean.txt

# 3. Generate TTS (using macOS say with good voice)
say -v Daniel -r 180 -f narration-clean.txt -o loomdemo-voiceover.aiff
ffmpeg -i loomdemo-voiceover.aiff loomdemo-voiceover.mp3

# 4. Combine
ffmpeg -i loomdemo-silent.mp4 -i loomdemo-voiceover.mp3 \
  -c:v copy -c:a aac -shortest \
  loomdemo-final.mp4
```

### Professional (Record Your Voice)

```bash
# 1. Convert to MP4 (same as above)
agg --fps-cap 30 --speed 1.0 loomdemo loomdemo-hq.gif
ffmpeg -i loomdemo-hq.gif -movflags faststart -pix_fmt yuv420p \
  loomdemo-silent.mp4

# 2. Record voiceover using QuickTime or Audacity
#    Read from loomdemo-narration.md
#    Save as loomdemo-voiceover.mp3

# 3. Import both into iMovie/DaVinci Resolve
#    Sync using timestamps in narration script:
#    - 0:00-0:30: Introduction
#    - 0:30-1:45: Discovering Weaver
#    - 1:45-2:30: Weaver in Action (show tool executions)
#    - 2:30-4:00: Table Discovery (highlight error #1)
#    - 4:00-6:00: Geospatial Analysis (highlight error #2)
#    - 6:00-6:28: Closing

# 4. Export from video editor
```

---

## Pro Tips for Voiceover Narration

### Recording Tips
1. **Use your narration script** - Read from `loomdemo-narration.md`
2. **Mark beats** - Note timestamps where key actions happen
3. **Pause for errors** - Let the error message display for 2-3 seconds
4. **Speed up boring parts** - Fast-forward repetitive shell_execute calls
5. **Record in segments** - Record each "Act" separately, easier to re-do

### Voice Settings (if using TTS)
```bash
# List available voices
say -v ?

# Good voices for tech demos:
say -v Daniel    # American English, clear
say -v Samantha  # American English, professional
say -v Karen     # Australian English
say -v Daniel -r 180  # Faster speech rate (default 175)
```

### Audio Quality
- **Reduce background noise** - Record in quiet room
- **Use pop filter** - Or position mic off-axis
- **Normalize audio** - Use Audacity's Normalize effect
- **Add subtle music** - Use royalty-free background track (optional)

---

## Advanced: Multi-Track Audio

For professional results, layer multiple audio tracks:

```bash
# Track 1: Voiceover (main)
# Track 2: Background music (low volume)
# Track 3: Sound effects (error beeps, success dings)

ffmpeg -i loomdemo-silent.mp4 \
  -i voiceover.mp3 \
  -i background-music.mp3 \
  -filter_complex \
    "[1:a]volume=1.0[vo]; \
     [2:a]volume=0.2[music]; \
     [vo][music]amix=inputs=2:duration=first[aout]" \
  -map 0:v -map "[aout]" \
  -c:v copy -c:a aac \
  loomdemo-final.mp4
```

---

## Recommended Final Specs

For uploading to YouTube/Twitter/LinkedIn:

```bash
# Export settings:
- Container: MP4
- Video codec: H.264 (libx264)
- Video bitrate: 5000 kbps
- Resolution: 1920x1080 (or original)
- Frame rate: 30 fps
- Audio codec: AAC
- Audio bitrate: 192 kbps
- Audio sample rate: 48000 Hz
```

**ffmpeg command for final export:**
```bash
ffmpeg -i loomdemo-with-audio.mp4 \
  -c:v libx264 -preset slow -crf 22 \
  -c:a aac -b:a 192k -ar 48000 \
  -vf "scale=1920:1080:force_original_aspect_ratio=decrease,pad=1920:1080:(ow-iw)/2:(oh-ih)/2" \
  -movflags +faststart \
  loomdemo-youtube.mp4
```

---

## Quick Reference Commands

```bash
# Convert asciinema to MP4
agg --fps-cap 30 loomdemo loomdemo.gif
ffmpeg -i loomdemo.gif loomdemo-silent.mp4

# Record voiceover (QuickTime)
# File → New Audio Recording → Save as voiceover.m4a

# Add voiceover to video
ffmpeg -i loomdemo-silent.mp4 -i voiceover.m4a \
  -c:v copy -c:a aac -shortest \
  loomdemo-final.mp4

# Or use iMovie (drag & drop, sync, export)
```

---

## Troubleshooting

**Audio and video don't sync:**
- Use video editor (iMovie/DaVinci) for manual sync
- Or add delay: `adelay=5000|5000` (5 seconds)

**Video is too long after adding audio:**
- Use `-shortest` flag to cut when shortest stream ends
- Or trim video: `ffmpeg -i input.mp4 -ss 00:00:05 -to 00:06:28 -c copy output.mp4`

**Audio quality is poor:**
- Record in WAV format first (lossless)
- Use `-b:a 320k` for higher audio bitrate
- Use noise reduction in Audacity

**File size too large:**
- Reduce video bitrate: `-b:v 3000k`
- Reduce resolution: `-vf scale=1280:720`
- Use H.265: `-c:v libx265` (smaller, but slower encode)

---

## Next Steps for Your loomdemo

1. ✅ Convert to MP4: `agg loomdemo loomdemo.gif` → `ffmpeg -i loomdemo.gif loomdemo-silent.mp4`
2. 🎤 Record voiceover using your `loomdemo-narration.md` script
3. 🎬 Sync in iMovie or use ffmpeg
4. 📤 Export and share!

**Estimated time:**
- Recording voiceover: 15-30 minutes (with re-takes)
- Syncing in iMovie: 10-20 minutes
- Exporting: 5-10 minutes
- **Total: ~1 hour for professional result**
