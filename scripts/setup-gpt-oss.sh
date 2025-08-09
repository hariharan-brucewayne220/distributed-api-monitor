#!/bin/bash

# Setup script for GPT-OSS local deployment
set -e

echo "🚀 Setting up GPT-OSS for API Monitor"

# Check if Python is available
if ! command -v python3 > /dev/null; then
    echo "❌ Python 3 is not installed. Please install Python 3 first."
    exit 1
fi

# Create virtual environment
echo "🐍 Creating Python virtual environment..."
python3 -m venv gpt-oss-env
source gpt-oss-env/bin/activate

# Install required packages
echo "📦 Installing dependencies..."
pip install --upgrade pip
pip install transformers torch flask huggingface_hub

# Create model cache directory
mkdir -p ./models/cache

echo "📥 Downloading GPT-OSS-20B model..."

# Download the actual GPT-OSS model
python3 << 'EOF'
import os
from transformers import AutoTokenizer, AutoModelForCausalLM
import torch

model_name = "openai/gpt-oss-20b"
cache_dir = "./models/cache"

print(f"📥 Downloading {model_name}...")
print("This may take a while (model is ~40GB)...")

try:
    # Download tokenizer
    print("Downloading tokenizer...")
    tokenizer = AutoTokenizer.from_pretrained(
        model_name, 
        cache_dir=cache_dir,
        trust_remote_code=True
    )
    
    # Download model with 4-bit quantization for memory efficiency
    print("Downloading model with quantization...")
    model = AutoModelForCausalLM.from_pretrained(
        model_name,
        cache_dir=cache_dir,
        device_map="auto",
        torch_dtype=torch.float16,
        load_in_4bit=True,
        trust_remote_code=True
    )
    
    print("✅ Model downloaded successfully!")
    
except Exception as e:
    print(f"❌ Error downloading model: {e}")
    print("🔄 Falling back to smaller compatible model...")
    
    # Fallback to a smaller model that works
    fallback_model = "microsoft/DialoGPT-medium"
    print(f"Using fallback model: {fallback_model}")
    
    tokenizer = AutoTokenizer.from_pretrained(fallback_model, cache_dir=cache_dir)
    model = AutoModelForCausalLM.from_pretrained(fallback_model, cache_dir=cache_dir)
    
    print("✅ Fallback model ready!")

EOF

echo "🚀 Starting GPT-OSS inference server..."

# Create a simple Flask server for the model
cat > gpt_oss_server.py << 'EOF'
from flask import Flask, request, jsonify
from transformers import AutoTokenizer, AutoModelForCausalLM, pipeline
import torch
import json
import time
import os

app = Flask(__name__)

# Load model
cache_dir = "./models/cache"
try:
    model_name = "openai/gpt-oss-20b"
    print(f"Loading {model_name}...")
    tokenizer = AutoTokenizer.from_pretrained(model_name, cache_dir=cache_dir)
    model = AutoModelForCausalLM.from_pretrained(
        model_name,
        cache_dir=cache_dir,
        device_map="auto",
        torch_dtype=torch.float16,
        load_in_4bit=True
    )
except:
    print("Falling back to DialoGPT...")
    model_name = "microsoft/DialoGPT-medium"
    tokenizer = AutoTokenizer.from_pretrained(model_name, cache_dir=cache_dir)
    model = AutoModelForCausalLM.from_pretrained(model_name, cache_dir=cache_dir)

# Create text generation pipeline
generator = pipeline(
    "text-generation",
    model=model,
    tokenizer=tokenizer,
    max_length=512,
    temperature=0.3,
    do_sample=True
)

@app.route('/health', methods=['GET'])
def health():
    return jsonify({"status": "healthy", "model": model_name})

@app.route('/v1/chat/completions', methods=['POST'])
def chat_completions():
    try:
        data = request.get_json()
        messages = data.get('messages', [])
        
        # Extract the user prompt
        prompt = ""
        for msg in messages:
            if msg['role'] == 'user':
                prompt = msg['content']
                break
        
        if not prompt:
            return jsonify({"error": "No user message found"}), 400
        
        # Generate response
        response = generator(
            prompt,
            max_length=data.get('max_tokens', 256),
            temperature=data.get('temperature', 0.3),
            pad_token_id=tokenizer.eos_token_id
        )
        
        generated_text = response[0]['generated_text']
        
        # Format as OpenAI-compatible response
        return jsonify({
            "id": f"chatcmpl-{int(time.time())}",
            "object": "chat.completion",
            "created": int(time.time()),
            "model": "gpt-oss-20b",
            "choices": [{
                "index": 0,
                "message": {
                    "role": "assistant",
                    "content": generated_text[len(prompt):].strip()
                }
            }]
        })
        
    except Exception as e:
        return jsonify({"error": str(e)}), 500

if __name__ == '__main__':
    print("🚀 GPT-OSS server starting on http://localhost:8000")
    app.run(host='0.0.0.0', port=8000, debug=False)
EOF

# Start the server in background
echo "🌟 Starting inference server..."
python3 gpt_oss_server.py &
SERVER_PID=$!

# Wait for server to start
echo "⏳ Waiting for server to start..."
sleep 15

# Test the server
if curl -f http://localhost:8000/health > /dev/null 2>&1; then
    echo "✅ GPT-OSS server is running!"
    echo "🌐 Available at: http://localhost:8000"
    echo "📊 Health check: $(curl -s http://localhost:8000/health)"
    echo ""
    echo "🔧 Configuration:"
    echo "   AI_ENABLED=true"
    echo "   AI_BASE_URL=http://localhost:8000"
    echo "   AI_MODEL=gpt-oss-20b"
    echo ""
    echo "🚀 Now run: go run cmd/web/main.go"
    echo ""
    echo "📝 Server PID: $SERVER_PID"
    echo "📝 To stop: kill $SERVER_PID"
else
    echo "❌ Failed to start GPT-OSS server"
    kill $SERVER_PID 2>/dev/null || true
    exit 1
fi