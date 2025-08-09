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