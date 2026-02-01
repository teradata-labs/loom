#!/bin/bash
# Generate TTS narration from the narration script

set -e

echo "🤖 Generating TTS narration from loomdemo-narration.md..."
echo ""

# Extract clean narration text (remove markdown formatting)
echo "📝 Extracting narration text..."
grep '^> ' loomdemo-narration.md | \
    sed 's/^> "//' | \
    sed 's/"$//' | \
    grep -v '^\[' | \
    grep -v '^-' > narration-clean.txt

echo "✓ Extracted narration to narration-clean.txt"
echo ""

# List available voices
echo "Available voices on your system:"
say -v ? | grep en_ | head -10
echo ""

# Generate TTS
echo "🎤 Generating TTS audio..."
echo "Using voice: Daniel (American English, professional)"
echo ""

say -v Daniel -r 180 -f narration-clean.txt -o loomdemo-voiceover.aiff

# Convert to MP3
echo "Converting to MP3..."
ffmpeg -i loomdemo-voiceover.aiff \
    -acodec libmp3lame \
    -ab 192k \
    -ar 48000 \
    -y \
    loomdemo-voiceover.mp3

echo "✓ Created loomdemo-voiceover.mp3"
echo ""

# Show duration
DURATION=$(ffmpeg -i loomdemo-voiceover.mp3 2>&1 | grep Duration | awk '{print $2}' | tr -d ,)
echo "📊 Audio duration: $DURATION"
echo ""

# Clean up
rm loomdemo-voiceover.aiff

echo "✅ TTS narration generated!"
echo ""
echo "📤 Next steps:"
echo "  1. Listen: open loomdemo-voiceover.mp3"
echo "  2. If it sounds good, run: ./create-video-with-vo.sh"
echo "  3. If you want better quality, record your own voice:"
echo "     - Open QuickTime: File → New Audio Recording"
echo "     - Read from loomdemo-narration.md"
echo "     - Save as loomdemo-voiceover.m4a (will override this TTS)"
echo ""
echo "⚠️  Note: TTS voice is robotic. Human voice is much better for demos!"
