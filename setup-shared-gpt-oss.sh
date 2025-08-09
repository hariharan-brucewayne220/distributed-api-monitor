#!/bin/bash
set -e

echo "🚀 Setting up GPT-OSS-120B in shared models directory"
echo "===================================================="

# Create shared models directory
SHARED_DIR="/mnt/d/claude-projects/shared-models"
mkdir -p "$SHARED_DIR"
cd "$SHARED_DIR"

echo "📁 Working in: $(pwd)"

# Create virtual environment
echo "🐍 Creating virtual environment..."
python3 -m venv gpt-oss-env
source gpt-oss-env/bin/activate

# Install required packages
echo "📦 Installing packages..."
pip install --upgrade pip
pip install huggingface_hub
pip install gpt-oss

# Download the model
echo "📥 Downloading GPT-OSS-120B model (this will take a while)..."
huggingface-cli download openai/gpt-oss-120b --include "original/*" --local-dir gpt-oss-120b/

echo "✅ GPT-OSS-120B model downloaded successfully!"
echo "📁 Model location: $SHARED_DIR/gpt-oss-120b/"
echo "🔧 Virtual environment: $SHARED_DIR/gpt-oss-env/"

# Test the model
echo "🧪 Testing model chat interface..."
cd gpt-oss-120b/
python -m gpt_oss.chat model/ &
CHAT_PID=$!

sleep 5

echo "🎉 Setup complete!"
echo "📋 Usage:"
echo "   Model path: $SHARED_DIR/gpt-oss-120b/"
echo "   Virtual env: source $SHARED_DIR/gpt-oss-env/bin/activate"
echo "   Chat test: python -m gpt_oss.chat model/"
echo ""
echo "🔗 This model is now available for all claude-projects!"

# Clean up test
kill $CHAT_PID 2>/dev/null || true