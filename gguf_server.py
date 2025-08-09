from flask import Flask, request, jsonify
from llama_cpp import Llama
import json
import time
import os

app = Flask(__name__)

# Load configuration from environment
model_path = os.getenv("MODEL_PATH", "/models/gpt-oss-20b-MXFP4.gguf")
num_threads = int(os.getenv("THREADS", "4"))
context_size = int(os.getenv("CTX", "2048"))
gpu_layers = int(os.getenv("GPU_LAYERS", "0"))  # 0 = CPU-only
port = int(os.getenv("PORT", "8000"))

print(f"🔄 Loading GGUF model from: {model_path}")
try:
    llm = Llama(
        model_path=model_path,
        n_ctx=context_size,
        n_threads=num_threads,
        n_gpu_layers=gpu_layers,
        verbose=True,
        use_mmap=True,
        use_mlock=False,
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
    print(f"🚀 GGUF server starting on http://localhost:{port}")
    print(f"📁 Model: {model_path}")
    app.run(host='0.0.0.0', port=port, debug=False)