#!/bin/bash
# Convert loomdemo asciinema recording to GIF

set -e

echo "🎬 Converting loomdemo to GIF..."

# Method 1: Using agg (recommended)
# Install: brew install agg
if command -v agg &> /dev/null; then
    echo "✓ Using agg (high quality)"

    # Full quality, no speed adjustment
    agg loomdemo loomdemo.gif
    echo "✓ Created loomdemo.gif (full quality)"

    # With speed adjustment (1.5x faster)
    agg --speed 1.5 loomdemo loomdemo-fast.gif
    echo "✓ Created loomdemo-fast.gif (1.5x speed)"

    # With custom theme and size
    agg --theme monokai --cols 120 --rows 30 loomdemo loomdemo-compact.gif
    echo "✓ Created loomdemo-compact.gif (compact, themed)"

    ls -lh loomdemo*.gif
    exit 0
fi

# Method 2: Using docker with agg
if command -v docker &> /dev/null; then
    echo "✓ Using agg via docker"
    docker run --rm -v "$PWD:/data" asciinema/agg /data/loomdemo /data/loomdemo.gif
    echo "✓ Created loomdemo.gif"
    ls -lh loomdemo.gif
    exit 0
fi

# Method 3: Using asciicast2gif (if installed)
if command -v asciicast2gif &> /dev/null; then
    echo "✓ Using asciicast2gif"
    asciicast2gif -s 2 loomdemo loomdemo.gif
    echo "✓ Created loomdemo.gif"
    ls -lh loomdemo.gif
    exit 0
fi

# If nothing is available, provide instructions
echo "❌ No conversion tool found!"
echo ""
echo "Install one of these:"
echo ""
echo "Option 1 (Recommended): Install agg"
echo "  brew install agg"
echo ""
echo "Option 2: Use Docker"
echo "  docker run --rm -v \"\$PWD:/data\" asciinema/agg /data/loomdemo /data/loomdemo.gif"
echo ""
echo "Option 3: Install asciicast2gif"
echo "  npm install -g asciicast2gif"
echo ""
exit 1
