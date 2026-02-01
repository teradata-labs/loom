#!/bin/bash
# Convert asciinema recording directly to high-quality MP4 video
# Skips GIF intermediate for better quality

set -e

if [ $# -lt 1 ]; then
    echo "Usage: ./asciinema-to-video.sh input.cast [output.mp4] [fps]"
    echo ""
    echo "Examples:"
    echo "  ./asciinema-to-video.sh loomdemo-final.cast"
    echo "  ./asciinema-to-video.sh loomdemo-final.cast demo.mp4"
    echo "  ./asciinema-to-video.sh loomdemo-final.cast demo.mp4 60"
    echo ""
    echo "Creates high-quality MP4 ready for:"
    echo "  - Adding voiceover"
    echo "  - Uploading to YouTube/Vimeo"
    echo "  - Professional presentations"
    exit 1
fi

INPUT="$1"
OUTPUT="${2:-${INPUT%.cast}.mp4}"
FPS="${3:-30}"

# Check dependencies
command -v agg >/dev/null 2>&1 || { echo "❌ agg not installed. Run: brew install agg"; exit 1; }
command -v ffmpeg >/dev/null 2>&1 || { echo "❌ ffmpeg not installed. Run: brew install ffmpeg"; exit 1; }

echo "🎬 Converting asciinema to high-quality MP4..."
echo ""
echo "📹 Input:  $INPUT"
echo "🎥 Output: $OUTPUT"
echo "🎞️  FPS:    $FPS"
echo ""

# Step 1: Render with agg to high-quality GIF (temporary)
echo "⏳ Step 1/3: Rendering terminal with agg (${FPS} FPS)..."
TEMP_GIF="${INPUT%.cast}-temp-hq.gif"

agg --fps-cap "$FPS" --theme dracula "$INPUT" "$TEMP_GIF"

echo "✓ Rendered to temporary GIF"
ls -lh "$TEMP_GIF" | awk '{print "   Size:", $5}'
echo ""

# Step 2: Convert GIF to MP4 with high quality settings
echo "⏳ Step 2/3: Converting to MP4 (CRF 18 = near-lossless)..."

ffmpeg -i "$TEMP_GIF" \
    -movflags faststart \
    -pix_fmt yuv420p \
    -c:v libx264 \
    -crf 18 \
    -preset slow \
    -vf "scale=trunc(iw/2)*2:trunc(ih/2)*2" \
    -y \
    "$OUTPUT" 2>&1 | grep -E "(frame=|size=|time=)" || true

echo ""
echo "✓ Converted to MP4"
ls -lh "$OUTPUT" | awk '{print "   Size:", $5}'

# Get video info
DURATION=$(ffmpeg -i "$OUTPUT" 2>&1 | grep Duration | awk '{print $2}' | tr -d ,)
echo "   Duration: $DURATION"
echo ""

# Step 3: Clean up temporary GIF
echo "⏳ Step 3/3: Cleaning up temporary files..."
rm "$TEMP_GIF"
echo "✓ Removed temporary GIF"
echo ""

# Summary
echo "✅ Done!"
echo ""
echo "📊 Video specs:"
ffmpeg -i "$OUTPUT" 2>&1 | grep -E "(Stream|Duration)" | head -3
echo ""
echo "📤 Next steps:"
echo "  1. Preview: open $OUTPUT"
echo "  2. Add voiceover: ./create-video-with-vo.sh"
echo "  3. Or edit in iMovie/DaVinci Resolve"
echo ""
echo "🎉 High-quality MP4 ready for voiceover!"
