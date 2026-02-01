# Editing GIF Recordings

Preview.app **cannot** edit GIFs (only view them). Here are your options:

---

## Option 1: Re-render with agg (Easiest)

The simplest approach is to trim the **source** asciinema file and re-render:

### Trim the asciinema recording

```bash
# Install asciinema utilities
brew install asciinema

# Cut from start (skip first 30 seconds)
asciinema cut --start 30 loomdemo loomdemo-trimmed

# Cut from end (stop at 6 minutes)
asciinema cut --end 360 loomdemo loomdemo-trimmed

# Cut both (30s to 6min)
asciinema cut --start 30 --end 360 loomdemo loomdemo-trimmed

# Then re-render
agg --speed 1.3 --fps-cap 15 --theme dracula loomdemo-trimmed loomdemo.gif
```

### Speed up/slow down sections

Unfortunately, `asciinema cut` can't do variable speed. For that, you need video editing (see Option 3).

---

## Option 2: Edit GIF with gifsicle (Command-line)

`gifsicle` can delete frames, but it's frame-by-frame (tedious for 6+ minute recordings).

```bash
brew install gifsicle

# Delete specific frame range (frames 100-150)
gifsicle loomdemo.gif --delete '#100-#150' -o loomdemo-edited.gif

# Keep only frames 50-300
gifsicle loomdemo.gif '#50-#300' -o loomdemo-edited.gif

# Optimize (reduce size, no edits)
gifsicle -O3 loomdemo.gif -o loomdemo-optimized.gif
```

**Problem:** Hard to know which frame numbers to target without viewing frame-by-frame.

---

## Option 3: Convert to Video, Edit, Convert Back (Recommended)

This gives you the full power of video editing (iMovie/DaVinci).

### Step 1: Convert GIF to MP4

```bash
ffmpeg -i loomdemo.gif -movflags faststart -pix_fmt yuv420p loomdemo.mp4
```

### Step 2: Edit in iMovie

1. Open iMovie
2. Import `loomdemo.mp4`
3. **Trim beginning/end:** Drag yellow handles on clip edges
4. **Cut middle sections:** Position playhead, press Cmd+B to split, delete unwanted parts
5. **Speed up boring parts:** Select clip, click speedometer icon, choose 2x or custom speed
6. **Add freeze frames:** Right-click frame, "Add Freeze Frame"
7. File → Share → File → Export as MP4

### Step 3: Convert back to GIF (if needed)

```bash
# Convert edited video back to GIF
ffmpeg -i loomdemo-edited.mp4 \
    -vf "fps=15,scale=1280:-1:flags=lanczos,split[s0][s1];[s0]palettegen[p];[s1][p]paletteuse" \
    -loop 0 \
    loomdemo-edited.gif
```

**Or just keep it as MP4 for voiceover!** (Videos support audio, GIFs don't)

---

## Option 4: Use Photoshop (GUI, Frame-by-Frame)

If you have Photoshop:

1. File → Import → Video Frames to Layers
2. Select `loomdemo.gif`
3. Edit timeline:
   - Delete frames (select, click trash)
   - Duplicate frames (copy/paste)
   - Adjust timing per frame
4. File → Export → Save for Web → GIF

**Pros:** Full frame control, visual editing
**Cons:** Expensive, slow for long recordings

---

## Option 5: Online GIF Editors (Quick & Easy)

For simple edits (trim, crop, speed):

### ezgif.com (Free, no account needed)

**Trim:**
1. Upload GIF: https://ezgif.com/cut
2. Set start/end times or frame numbers
3. Click "Cut GIF!"

**Speed:**
1. Upload GIF: https://ezgif.com/speed
2. Set speed multiplier (e.g., 1.5x = 50% faster)
3. Apply

**Crop:**
1. Upload GIF: https://ezgif.com/crop
2. Draw crop box
3. Crop

**Optimize:**
1. Upload GIF: https://ezgif.com/optimize
2. Compression level
3. Optimize

---

## Recommended Workflow for Your Demo

Based on your 6:28 recording and narration script:

### Quick Edits (No Audio)

**If you just want to trim/speed up the GIF:**

```bash
# Option A: Trim asciinema source, re-render
asciinema cut --start 5 --end 380 loomdemo loomdemo-trimmed
agg --speed 1.3 --fps-cap 15 --theme dracula loomdemo-trimmed loomdemo.gif

# Option B: Use ezgif.com to trim the GIF online
# Upload loomdemo.gif → Cut tool → Set start/end → Done
```

### Professional Edit (With Voiceover)

**Convert to video, edit in iMovie:**

```bash
# 1. Convert GIF to video
ffmpeg -i loomdemo.gif -pix_fmt yuv420p loomdemo.mp4

# 2. Edit in iMovie:
#    - Import loomdemo.mp4
#    - Trim boring parts (repetitive shell_execute calls)
#    - Speed up sections (1.5x for discovery phase)
#    - Add voiceover track
#    - Export as MP4

# 3. No need to convert back to GIF - keep as MP4 for YouTube/social!
```

---

## Common Edits You Might Want

### Trim the Beginning (Skip Initial Load)

```bash
# Skip first 5 seconds
asciinema cut --start 5 loomdemo loomdemo-trimmed
agg loomdemo-trimmed loomdemo.gif
```

### Speed Up Boring Parts

**Using iMovie (best):**
1. Import video
2. Select slow section (e.g., repetitive tool calls)
3. Click speedometer icon
4. Choose 2x speed

**Using ffmpeg (video only, not GIF):**
```bash
# Speed up entire video 1.5x
ffmpeg -i loomdemo.mp4 -filter:v "setpts=0.67*PTS" -an loomdemo-fast.mp4
```

### Remove Middle Section

**Using iMovie:**
1. Position playhead at cut start
2. Press Cmd+B (split)
3. Move to cut end
4. Press Cmd+B (split)
5. Select middle section, press Delete

**Using ffmpeg (video):**
```bash
# Remove 2:00-3:00 (keep 0:00-2:00 and 3:00-end)
ffmpeg -i loomdemo.mp4 \
    -filter_complex "[0:v]trim=0:120,setpts=PTS-STARTPTS[v1]; \
                     [0:v]trim=180,setpts=PTS-STARTPTS[v2]; \
                     [v1][v2]concat=n=2:v=1[out]" \
    -map "[out]" loomdemo-cut.mp4
```

---

## Pro Tips

### If You're Adding Voiceover...

**Don't bother with GIF!** Just edit the video:
1. Convert to MP4 (see above)
2. Edit in iMovie (trim, speed, cut)
3. Add voiceover track
4. Export as MP4 (YouTube/social ready)

GIFs don't support audio anyway, so editing as video gives you way more control.

### For README/Docs (No Audio)

Use online tools for quick edits:
- **ezgif.com** - Trim, speed, optimize
- **gifcompressor.com** - Reduce file size
- **iloveimg.com/compress-image/compress-gif** - Compress

### For Presentations

Edit in iMovie/DaVinci for precise control:
- Trim dead time
- Speed up repetitive parts (1.5-2x)
- Add freeze frames for emphasis
- Add text overlays (optional)

---

## Quick Reference

| Task | Tool | Command |
|------|------|---------|
| Trim start/end | asciinema | `asciinema cut --start 30 --end 360 input output` |
| Delete frames | gifsicle | `gifsicle input.gif --delete '#100-#150' -o output.gif` |
| Speed up | iMovie | Import → Select clip → Speed icon → 2x |
| Cut section | iMovie | Cmd+B to split → Delete middle section |
| Compress | gifsicle | `gifsicle -O3 --colors 192 input.gif -o output.gif` |
| Crop | ezgif.com | Upload → Crop tool → Draw box → Crop |

---

## Troubleshooting

**GIF is too large to upload online:**
```bash
# Compress first
gifsicle -O3 --lossy=80 loomdemo.gif -o loomdemo-small.gif
```

**Need precise frame numbers:**
```bash
# Count total frames
gifsicle --info loomdemo.gif | grep "images"

# View specific frame as PNG
gifsicle loomdemo.gif '#100' -o frame-100.png
```

**Video edit changed timing for voiceover:**
Re-sync in iMovie's timeline after all video edits are done.

---

## My Recommendation for Your Demo

**For final video with voiceover:**

1. ✅ Keep the current GIF as-is for quick preview
2. ✅ Convert to MP4: `ffmpeg -i loomdemo.gif loomdemo.mp4`
3. ✅ Edit in iMovie:
   - Trim first 3-5 seconds (terminal startup)
   - Speed up repetitive shell_execute sections (1.5x)
   - Keep error moments at normal speed (important!)
   - Add voiceover track
4. ✅ Export as MP4 (no need for GIF)

**For README/docs (no audio, just visual):**

1. ✅ Upload current GIF to ezgif.com/cut
2. ✅ Trim first 5 seconds
3. ✅ Optimize with ezgif.com/optimize
4. ✅ Download and use

**Total time:** 5-10 minutes for online edit, 30 minutes for professional iMovie edit.
