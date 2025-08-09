#!/bin/bash
set -e

echo "🚀 Downloading GPT-OSS-120B to shared models directory"
echo "====================================================="

# Create shared models directory
SHARED_DIR="/mnt/d/claude-projects/shared-models"
mkdir -p "$SHARED_DIR"

echo "📁 Target directory: $SHARED_DIR"

# Try to install packages directly
echo "📦 Installing required packages..."

# Check if pip is available
if command -v pip3 >/dev/null 2>&1; then
    PIP_CMD="pip3"
elif command -v pip >/dev/null 2>&1; then
    PIP_CMD="pip"
else
    echo "❌ pip not found, trying with python -m pip"
    PIP_CMD="python3 -m pip"
fi

echo "Using: $PIP_CMD"

# Install packages
$PIP_CMD install --user huggingface_hub[cli]
$PIP_CMD install --user gpt-oss

# Add ~/.local/bin to PATH if not already there
export PATH="$HOME/.local/bin:$PATH"

# Download the model
echo "📥 Downloading GPT-OSS-120B model..."
echo "⏱️  This will take 30-60 minutes and use ~40GB of space"

cd "$SHARED_DIR"

if command -v huggingface-cli >/dev/null 2>&1; then
    huggingface-cli download openai/gpt-oss-120b --include "original/*" --local-dir gpt-oss-120b/
else
    echo "🔧 huggingface-cli not found in PATH, trying full path..."
    ~/.local/bin/huggingface-cli download openai/gpt-oss-120b --include "original/*" --local-dir gpt-oss-120b/
fi

echo "✅ GPT-OSS-120B model downloaded successfully!"
echo "📁 Model location: $SHARED_DIR/gpt-oss-120b/"

# Create info file
cat > "$SHARED_DIR/GPT-OSS-120B-INFO.md" << EOF
# GPT-OSS-120B Model

## Location
- Model: $SHARED_DIR/gpt-oss-120b/
- Size: ~40GB
- Downloaded: $(date)

## Usage Commands
\`\`\`bash
# Test the model
cd $SHARED_DIR/gpt-oss-120b/
python -m gpt_oss.chat model/

# Use in projects
export GPT_OSS_MODEL_PATH="$SHARED_DIR/gpt-oss-120b/"
\`\`\`

## Integration
This model can be used by any project in claude-projects by referencing:
- Path: $SHARED_DIR/gpt-oss-120b/
- Command: python -m gpt_oss.chat model/
EOF

echo "📋 Model info saved to: $SHARED_DIR/GPT-OSS-120B-INFO.md"
echo "🎉 Setup complete! Model ready for use across all claude-projects"