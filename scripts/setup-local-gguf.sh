#!/bin/bash

# Setup script for local GGUF model deployment
set -e

echo "🚀 Setting up local GPT-OSS GGUF model for API Monitor"

# Define paths
SHARED_DIR="/mnt/d/claude-projects/shared-models"
GGUF_MODEL_PATH="$SHARED_DIR/gpt-oss-20b-GGUF/gpt-oss-20b-MXFP4.gguf"

# Check if the GGUF model exists
if [ ! -f "$GGUF_MODEL_PATH" ]; then
    echo "❌ GGUF model not found at: $GGUF_MODEL_PATH"
    echo "Please ensure the model is downloaded to the shared models directory"
    exit 1
fi

echo "✅ Found GGUF model at: $GGUF_MODEL_PATH"

# Check if Python is available
if ! command -v python3 > /dev/null; then
    echo "❌ Python 3 is not installed. Please install Python 3 first."
    exit 1
fi

# Create virtual environment for GGUF server
echo "🐍 Creating Python virtual environment for GGUF server..."
python3 -m venv gguf-server-env
source gguf-server-env/bin/activate

# Install required packages for GGUF
echo "📦 Installing GGUF dependencies..."
pip install --upgrade pip
pip install flask
pip install llama-cpp-python

echo "🚀 Starting GGUF inference server..."

# Create GGUF server script
cat > gguf_server.py << 'EOF'
from flask import Flask, request, jsonify
from llama_cpp import Llama
import json
import time
import os

app = Flask(__name__)

# Load GGUF model
model_path = "/mnt/d/claude-projects/shared-models/gpt-oss-20b-GGUF/gpt-oss-20b-MXFP4.gguf"

print(f"🔄 Loading GGUF model from: {model_path}")
try:
    llm = Llama(
        model_path=model_path,
        n_ctx=2048,  # Context window
        n_threads=4,  # CPU threads
        verbose=False
    )
    print("✅ GGUF model loaded successfully!")
except Exception as e:
    print(f"❌ Failed to load model: {e}")
    exit(1)

@app.route('/health', methods=['GET'])
def health():
    return jsonify({
        "status": "healthy", 
        "model": "gpt-oss-20b-gguf",
        "model_path": model_path
    })

@app.route('/v1/chat/completions', methods=['POST'])
def chat_completions():
    try:
        data = request.get_json()
        messages = data.get('messages', [])
        max_tokens = data.get('max_tokens', 256)
        temperature = data.get('temperature', 0.3)
        
        # Extract the user prompt
        prompt = ""
        system_prompt = ""
        
        for msg in messages:
            if msg['role'] == 'system':
                system_prompt = msg['content']
            elif msg['role'] == 'user':
                prompt = msg['content']
        
        if not prompt:
            return jsonify({"error": "No user message found"}), 400
        
        # Combine system and user prompts
        full_prompt = f"System: {system_prompt}\n\nUser: {prompt}\n\nAssistant:"
        
        # Generate response using GGUF model
        response = llm(
            full_prompt,
            max_tokens=max_tokens,
            temperature=temperature,
            stop=["User:", "System:", "\n\n"],
            echo=False
        )
        
        generated_text = response['choices'][0]['text'].strip()
        
        # Format as OpenAI-compatible response
        return jsonify({
            "id": f"chatcmpl-{int(time.time())}",
            "object": "chat.completion", 
            "created": int(time.time()),
            "model": "gpt-oss-20b-gguf",
            "choices": [{
                "index": 0,
                "message": {
                    "role": "assistant",
                    "content": generated_text
                }
            }]
        })
        
    except Exception as e:
        print(f"Error in chat completion: {e}")
        return jsonify({"error": str(e)}), 500

if __name__ == '__main__':
    print("🚀 GGUF server starting on http://localhost:8000")
    print(f"📁 Model: {model_path}")
    app.run(host='0.0.0.0', port=8000, debug=False)
EOF

# Start the server in background
echo "🌟 Starting GGUF inference server..."
python3 gguf_server.py &
SERVER_PID=$!

# Wait for server to start
echo "⏳ Waiting for server to start..."
sleep 10

# Test the server
if curl -f http://localhost:8000/health > /dev/null 2>&1; then
    echo "✅ GGUF server is running!"
    echo "🌐 Available at: http://localhost:8000"
    echo "📊 Health check: $(curl -s http://localhost:8000/health)"
    echo ""
    echo "🔧 Configuration for API Monitor:"
    echo "   export AI_ENABLED=true"
    echo "   export AI_BASE_URL=http://localhost:8000"
    echo "   export AI_MODEL=gpt-oss-20b-gguf"
    echo ""
    echo "🚀 Now run: go run cmd/web/main.go"
    echo ""
    echo "📝 Server PID: $SERVER_PID (save this to stop the server later)"
    echo "📝 To stop: kill $SERVER_PID"
    echo ""
    echo "💾 To stop cleanly: kill $SERVER_PID && deactivate"
else
    echo "❌ Failed to start GGUF server"
    kill $SERVER_PID 2>/dev/null || true
    exit 1
fi