# Converting Asciinema Recordings to GIF

## Quick Start

Since you have Docker installed, the easiest method is:

```bash
# Run the conversion script
./convert-to-gif.sh
```

Or directly with Docker:

```bash
docker run --rm -v "$PWD:/data" asciinema/agg /data/loomdemo /data/loomdemo.gif
```

---

## Method 1: Using `agg` (Recommended for Best Quality)

**Install:**
```bash
brew install agg
```

**Basic conversion:**
```bash
agg loomdemo loomdemo.gif
```

**Advanced options:**
```bash
# Adjust playback speed (1.5x faster)
agg --speed 1.5 loomdemo loomdemo-fast.gif

# Change theme
agg --theme monokai loomdemo loomdemo-monokai.gif

# Resize output
agg --cols 120 --rows 30 loomdemo loomdemo-compact.gif

# Limit FPS to reduce file size
agg --fps-cap 15 loomdemo loomdemo-optimized.gif
```

**Available themes:**
- asciinema
- dracula
- monokai
- solarized-dark
- solarized-light
- nord

---

## Method 2: Using Docker (No Installation Required)

**Basic conversion:**
```bash
docker run --rm -v "$PWD:/data" asciinema/agg /data/loomdemo /data/loomdemo.gif
```

**With options:**
```bash
# Speed up playback
docker run --rm -v "$PWD:/data" asciinema/agg \
  --speed 1.5 \
  /data/loomdemo /data/loomdemo-fast.gif

# Custom theme and size
docker run --rm -v "$PWD:/data" asciinema/agg \
  --theme monokai \
  --cols 120 \
  --rows 30 \
  /data/loomdemo /data/loomdemo-custom.gif
```

---

## Method 3: Using `asciicast2gif` (npm package)

**Install:**
```bash
npm install -g asciicast2gif
```

**Convert:**
```bash
asciicast2gif -s 2 loomdemo loomdemo.gif
```

**Options:**
- `-s <scale>` - Scale factor (1-4)
- `-t <theme>` - Color theme
- `-S <speed>` - Playback speed multiplier

---

## Method 4: Using `svg-term-cli` (SVG instead of GIF)

**Install:**
```bash
npm install -g svg-term-cli
```

**Convert to SVG (smaller file size, infinite quality):**
```bash
cat loomdemo | svg-term --out loomdemo.svg --window
```

**SVG advantages:**
- Smaller file size
- Infinite resolution (scalable)
- Can be embedded in web pages
- Can be converted to GIF later if needed

---

## Optimizing for Different Use Cases

### For README.md / GitHub (Balance size & quality)
```bash
agg --speed 1.2 --fps-cap 15 loomdemo loomdemo-readme.gif
```

### For Twitter / Social Media (Smaller file size)
```bash
agg --speed 1.5 --fps-cap 10 --cols 100 --rows 25 loomdemo loomdemo-social.gif
```

### For Presentations (High quality)
```bash
agg --fps-cap 30 loomdemo loomdemo-presentation.gif
```

### For Documentation (Compact)
```bash
agg --speed 1.3 --theme monokai --cols 120 --rows 30 loomdemo loomdemo-docs.gif
```

---

## Recommended Settings for loomdemo

Based on the 6:28 minute recording:

**Option A: Full quality for documentation**
```bash
agg --theme dracula --fps-cap 20 loomdemo loomdemo-full.gif
```

**Option B: Optimized for web (recommended)**
```bash
# Slightly faster playback, capped FPS, good quality
agg --speed 1.3 --fps-cap 15 --theme dracula loomdemo loomdemo-web.gif
```

**Option C: Compact for README**
```bash
# Faster playback, smaller size
agg --speed 1.5 --fps-cap 12 --cols 130 --rows 35 loomdemo loomdemo-readme.gif
```

---

## File Size Expectations

For a 6:28 minute recording:
- **Full quality (30 FPS):** 50-100 MB
- **Optimized (15 FPS, 1.3x speed):** 20-40 MB
- **Compact (12 FPS, 1.5x speed, smaller dims):** 10-20 MB
- **SVG:** 1-5 MB (but not animated loop in all viewers)

---

## Post-Processing: Optimize GIF Size

If the GIF is too large, optimize it:

**Using gifsicle:**
```bash
brew install gifsicle

# Optimize existing GIF (lossless)
gifsicle -O3 loomdemo.gif -o loomdemo-optimized.gif

# Reduce colors (lossy, smaller size)
gifsicle -O3 --colors 128 loomdemo.gif -o loomdemo-optimized.gif
```

**Using ImageMagick:**
```bash
brew install imagemagick

# Reduce size with quality tradeoff
convert loomdemo.gif -fuzz 10% -layers Optimize loomdemo-optimized.gif
```

---

## Troubleshooting

### GIF is too large
1. Reduce FPS: `--fps-cap 12`
2. Speed up playback: `--speed 1.5`
3. Reduce dimensions: `--cols 120 --rows 30`
4. Use gifsicle to optimize afterward

### Colors look wrong
Try different themes: `--theme dracula` or `--theme monokai`

### Docker permission errors
Make sure the current directory is accessible:
```bash
chmod -R 755 .
```

### agg not found after brew install
Restart terminal or run:
```bash
export PATH="/opt/homebrew/bin:$PATH"  # Apple Silicon
# or
export PATH="/usr/local/bin:$PATH"      # Intel Mac
```

---

## Quick Reference

| Tool | Install | Command |
|------|---------|---------|
| agg (native) | `brew install agg` | `agg loomdemo loomdemo.gif` |
| agg (docker) | Docker required | `docker run --rm -v "$PWD:/data" asciinema/agg /data/loomdemo /data/loomdemo.gif` |
| asciicast2gif | `npm install -g asciicast2gif` | `asciicast2gif -s 2 loomdemo loomdemo.gif` |
| svg-term | `npm install -g svg-term-cli` | `cat loomdemo \| svg-term --out loomdemo.svg` |

---

## Recommended Workflow for loomdemo

**For maximum quality + reasonable size:**

```bash
# Install agg
brew install agg

# Create optimized version for web/docs
agg --speed 1.3 --fps-cap 15 --theme dracula loomdemo loomdemo.gif

# Check size
ls -lh loomdemo.gif

# If too large, further optimize
gifsicle -O3 --colors 192 loomdemo.gif -o loomdemo-final.gif
```

**Expected result:** ~15-25 MB GIF with good quality and slightly faster playback (5 minute effective duration).
