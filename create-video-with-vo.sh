#!/bin/bash
# Create video with voiceover from asciinema recording

set -e

echo "🎬 Creating video with voiceover for loomdemo..."
echo ""

# Check dependencies
command -v agg >/dev/null 2>&1 || { echo "❌ agg not installed. Run: brew install agg"; exit 1; }
command -v ffmpeg >/dev/null 2>&1 || { echo "❌ ffmpeg not installed. Run: brew install ffmpeg"; exit 1; }

# Step 1: Convert asciinema to high-quality GIF
echo "📹 Step 1/4: Converting asciinema to video..."
if [ ! -f loomdemo-silent.mp4 ]; then
    # Render as high-FPS GIF first
    agg --fps-cap 30 --speed 1.0 loomdemo loomdemo-hq.gif

    # Convert to MP4
    ffmpeg -i loomdemo-hq.gif \
        -movflags faststart \
        -pix_fmt yuv420p \
        -vf "scale=trunc(iw/2)*2:trunc(ih/2)*2" \
        loomdemo-silent.mp4

    echo "✓ Created loomdemo-silent.mp4"
else
    echo "✓ loomdemo-silent.mp4 already exists"
fi

# Step 2: Check for voiceover audio
echo ""
echo "🎤 Step 2/4: Checking for voiceover audio..."
if [ -f loomdemo-voiceover.mp3 ] || [ -f loomdemo-voiceover.m4a ]; then
    echo "✓ Found voiceover audio"
    AUDIO_FILE=$(ls loomdemo-voiceover.* | head -1)
else
    echo "⚠️  No voiceover audio found!"
    echo ""
    echo "Options:"
    echo "  1. Record voiceover (recommended):"
    echo "     - Open QuickTime: File → New Audio Recording"
    echo "     - Read from loomdemo-narration.md"
    echo "     - Save as loomdemo-voiceover.m4a"
    echo ""
    echo "  2. Generate TTS (automated, lower quality):"
    echo "     - Run: ./generate-tts-narration.sh"
    echo ""
    echo "Re-run this script after creating audio file."
    exit 1
fi

# Step 3: Combine video and audio
echo ""
echo "🎞️  Step 3/4: Combining video and audio..."
ffmpeg -i loomdemo-silent.mp4 -i "$AUDIO_FILE" \
    -c:v copy \
    -c:a aac \
    -b:a 192k \
    -map 0:v:0 \
    -map 1:a:0 \
    -shortest \
    -y \
    loomdemo-final.mp4

echo "✓ Created loomdemo-final.mp4"

# Step 4: Create optimized versions
echo ""
echo "📦 Step 4/4: Creating optimized versions..."

# YouTube/social media optimized
ffmpeg -i loomdemo-final.mp4 \
    -c:v libx264 \
    -preset slow \
    -crf 22 \
    -c:a aac \
    -b:a 192k \
    -ar 48000 \
    -movflags +faststart \
    -y \
    loomdemo-youtube.mp4

echo "✓ Created loomdemo-youtube.mp4 (optimized for web)"

# Show results
echo ""
echo "✅ Done! Files created:"
echo ""
ls -lh loomdemo-silent.mp4 loomdemo-final.mp4 loomdemo-youtube.mp4
echo ""
echo "📤 Next steps:"
echo "  - Preview: open loomdemo-final.mp4"
echo "  - Upload to YouTube: loomdemo-youtube.mp4"
echo "  - Share on social media: loomdemo-youtube.mp4"
echo ""
echo "🎉 Your demo video with voiceover is ready!"
