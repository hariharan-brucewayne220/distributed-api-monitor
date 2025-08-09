#!/usr/bin/env python3
"""
Quick AI Demo Server for API Monitor
Uses a lightweight local model for immediate demo purposes
"""

from flask import Flask, request, jsonify
import json
import time
import random

app = Flask(__name__)

# Mock GPT-OSS responses for demo
class MockGPTOSS:
    def generate_insights(self, prompt):
        """Generate realistic monitoring insights based on the prompt"""
        
        if "endpoint" in prompt.lower() and ("down" in prompt.lower() or "unhealthy" in prompt.lower()):
            return [{
                "title": "🚨 Critical Service Disruption",
                "content": "Multiple endpoints are experiencing downtime. This appears to be a cascading failure affecting dependent services. Immediate investigation of network connectivity and upstream dependencies is recommended.",
                "type": "alert",
                "confidence": 0.95
            }, {
                "title": "📊 Pattern Analysis",
                "content": "The failure pattern suggests a potential DNS resolution issue or load balancer misconfiguration. Similar patterns were observed 3 weeks ago during the infrastructure update.",
                "type": "warning", 
                "confidence": 0.82
            }]
        
        elif "response time" in prompt.lower() or "slow" in prompt.lower():
            return [{
                "title": "⚠️ Performance Degradation Detected",
                "content": "Response times have increased by 340% compared to the baseline. This correlates with increased database query execution times and suggests potential indexing issues or connection pool exhaustion.",
                "type": "warning",
                "confidence": 0.88
            }, {
                "title": "💡 Optimization Recommendation",
                "content": "Consider implementing query result caching and reviewing database indexes. The /posts endpoint shows the highest latency increase, indicating a need for API-specific optimization.",
                "type": "info",
                "confidence": 0.76
            }]
        
        elif "healthy" in prompt.lower() and "200" in prompt:
            return [{
                "title": "✅ System Operating Optimally",
                "content": "All monitored endpoints are performing within expected parameters. Average response time of 234ms is 15% better than last week's baseline, indicating successful infrastructure optimizations.",
                "type": "success",
                "confidence": 0.92
            }, {
                "title": "📈 Trend Analysis",
                "content": "Performance has been consistently improving over the past 7 days. The GitHub API endpoint shows exceptional stability with 99.97% uptime. Consider this configuration as a template for other services.",
                "type": "info",
                "confidence": 0.84
            }]
        
        else:
            return [{
                "title": "📊 System Health Summary", 
                "content": "Based on current monitoring data, the system shows mixed performance indicators. While most endpoints are operational, there are opportunities for optimization in response time consistency.",
                "type": "info",
                "confidence": 0.78
            }, {
                "title": "🔍 Proactive Monitoring Insight",
                "content": "Consider implementing automated alert thresholds at 95th percentile response times to catch performance degradation before it impacts users. Current monitoring frequency is appropriate for production workloads.",
                "type": "info",
                "confidence": 0.71
            }]

mock_gpt = MockGPTOSS()

@app.route('/health', methods=['GET'])
def health():
    return jsonify({
        "status": "healthy", 
        "model": "gpt-oss-20b-demo",
        "type": "mock_ai_server",
        "capabilities": ["monitoring_insights", "pattern_analysis", "recommendations"]
    })

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
        
        # Generate insights based on prompt content
        insights = mock_gpt.generate_insights(prompt)
        
        # Format insights as JSON string for the response
        response_content = json.dumps(insights, indent=2)
        
        # Format as OpenAI-compatible response
        return jsonify({
            "id": f"chatcmpl-{int(time.time())}-demo",
            "object": "chat.completion", 
            "created": int(time.time()),
            "model": "gpt-oss-20b",
            "choices": [{
                "index": 0,
                "message": {
                    "role": "assistant",
                    "content": response_content
                }
            }]
        })
        
    except Exception as e:
        return jsonify({"error": str(e)}), 500

@app.route('/demo/test', methods=['GET'])
def demo_test():
    """Test endpoint to verify the server works"""
    sample_prompt = """
    Current endpoint status:
    - https://api.github.com/users/octocat: HEALTHY (Status: 200, Response Time: 245ms)
    - https://jsonplaceholder.typicode.com/posts/1: HEALTHY (Status: 200, Response Time: 156ms) 
    - https://httpbin.org/status/200: HEALTHY (Status: 200, Response Time: 892ms)
    - https://httpbin.org/delay/2: UNHEALTHY (Status: 0, Response Time: 5000ms, Error: timeout)
    """
    
    insights = mock_gpt.generate_insights(sample_prompt)
    return jsonify({
        "test": "success",
        "sample_insights": insights,
        "timestamp": time.time()
    })

if __name__ == '__main__':
    print("🚀 Quick AI Demo Server starting...")
    print("🤖 Mock GPT-OSS server for immediate demo")
    print("🌐 Health check: http://localhost:8000/health")
    print("🧪 Test endpoint: http://localhost:8000/demo/test") 
    print("📡 OpenAI-compatible API: http://localhost:8000/v1/chat/completions")
    print("")
    app.run(host='0.0.0.0', port=8000, debug=True)