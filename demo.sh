#!/bin/bash

echo "🚀 API Monitor Demo Setup"
echo "=========================="

# Check if Go is installed
if ! command -v go &> /dev/null; then
    echo "❌ Go is not installed. Please install Go first."
    echo "   Download from: https://golang.org/dl/"
    exit 1
fi

echo "✅ Go detected: $(go version)"

# Start the mock AI server
echo "🤖 Starting Mock AI Server..."
go run cmd/mock-ai/main.go &
AI_PID=$!
sleep 3

# Test AI server
echo "🧪 Testing AI Server..."
if curl -s http://localhost:8000/health > /dev/null; then
    echo "✅ AI Server is running!"
    echo "📊 Health check: $(curl -s http://localhost:8000/health | head -1)"
else
    echo "❌ AI Server failed to start"
    kill $AI_PID 2>/dev/null || true
    exit 1
fi

# Start the main web server
echo "🌐 Starting Web Dashboard..."
export AI_ENABLED=true
export AI_BASE_URL=http://localhost:8000
go run cmd/web/main.go &
WEB_PID=$!
sleep 3

# Test web server
echo "🧪 Testing Web Server..."
if curl -s http://localhost:8080/api/status > /dev/null; then
    echo "✅ Web Server is running!"
else
    echo "❌ Web Server failed to start"
    kill $AI_PID $WEB_PID 2>/dev/null || true
    exit 1
fi

echo ""
echo "🎉 Demo is ready!"
echo "=================="
echo "🌐 Web Dashboard: http://localhost:8080"
echo "🤖 AI Server: http://localhost:8000"
echo "📊 API Status: http://localhost:8080/api/status"
echo "🧠 AI Insights: http://localhost:8080/api/insights"
echo ""
echo "📝 Process IDs:"
echo "   AI Server: $AI_PID"
echo "   Web Server: $WEB_PID"
echo ""
echo "🛑 To stop demo: kill $AI_PID $WEB_PID"
echo ""
echo "🚀 Open http://localhost:8080 in your browser!"

# Keep script running
wait