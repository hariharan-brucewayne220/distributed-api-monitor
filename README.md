# 🚀 API Monitor

A distributed API monitoring system built with Go, featuring real-time web dashboard and AI-powered insights using OpenAI's GPT-OSS model.

## ✨ Features

- **Real-time Monitoring**: Concurrent HTTP endpoint health checks
- **Web Dashboard**: Beautiful, responsive interface with live updates
- **AI-Powered Insights**: Intelligent analysis using GPT-OSS model
- **PostgreSQL Storage**: Persistent historical data
- **gRPC Services**: Scalable microservices architecture
- **Docker Ready**: Easy deployment with Docker Compose
- **Configurable**: Environment-based configuration

## 🏗️ Architecture

```
┌─────────────────┐    ┌──────────────┐    ┌─────────────────┐
│   HTTP Checker  │───▶│  Web Server  │───▶│  Web Dashboard  │
└─────────────────┘    └──────────────┘    └─────────────────┘
         │                       │                    │
         ▼                       ▼                    ▼
┌─────────────────┐    ┌──────────────┐    ┌─────────────────┐
│   PostgreSQL    │    │  GPT-OSS AI  │    │  Real-time UI   │
└─────────────────┘    └──────────────┘    └─────────────────┘
```

## 🚀 Quick Start

### 🎯 Demo Mode (Recommended)
```bash
# One-command demo with AI
./demo.sh
```
Then open http://localhost:8080

### Option 1: Basic Setup (No AI)
```bash
# Start database
docker-compose up -d postgres

# Run web dashboard
go run cmd/web/main.go
```

### Option 2: Manual AI Setup
```bash
# Start mock AI server
go run cmd/mock-ai/main.go &

# Run with AI enabled
export AI_ENABLED=true
export AI_BASE_URL=http://localhost:8000
go run cmd/web/main.go
```

### Option 3: Local GGUF Model (Recommended)
```bash
# Use local GGUF model from shared directory (fast, efficient)
./scripts/setup-local-gguf.sh
```

### Option 4: Full GPT-OSS Model
```bash
# Real GPT-OSS model (requires GPU/lots of RAM)
./scripts/setup-gpt-oss.sh

# Or use Docker
docker-compose -f docker-compose.ai.yml up
```

## 🌐 Web Dashboard

Access the dashboard at: http://localhost:8080

- **Real-time status** of all monitored endpoints
- **Response time charts** with historical data  
- **AI insights** powered by GPT-OSS
- **Health metrics** and uptime statistics

## 🤖 AI Integration

The system uses **GPT-OSS-20B** model for intelligent monitoring insights:

- **Anomaly detection** in response patterns
- **Performance trend analysis**
- **Proactive recommendations**
- **Natural language summaries** of system health

## 📊 API Endpoints

- `GET /` - Web dashboard
- `GET /api/status` - Current endpoint status (JSON)
- `GET /api/insights` - AI-powered insights (JSON)

## ⚙️ Configuration

Environment variables:

```bash
# Database
DATABASE_URL="host=localhost port=5432 user=monitor password=password dbname=api_monitor sslmode=disable"

# Monitoring
CHECK_INTERVAL="15s"
REQUEST_TIMEOUT="5s"
WEB_PORT=8080

# AI (GPT-OSS)
AI_ENABLED=true
AI_BASE_URL="http://localhost:8000"
AI_API_KEY="your-api-key"
AI_MODEL="gpt-oss-20b"
```

## 🐳 Docker Setup

```bash
# Basic setup
docker-compose up

# With AI capabilities
docker-compose -f docker-compose.ai.yml up
```

## 💡 Demo Features

Perfect for showcasing:
- **Modern Go architecture** with clean separation
- **AI integration** with OpenAI's latest open-source model
- **Real-time web interfaces** with WebSocket-like updates
- **Concurrent monitoring** demonstrating Go's strengths
- **Container orchestration** with multi-service setup