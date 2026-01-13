# Vacation Planning Workflow

A multi-agent workflow for comprehensive vacation planning with real-time weather analysis using event-driven coordination.

## Overview

This workflow orchestrates three specialized agents to help users plan the perfect vacation:

1. **Vacation Coordinator** - User-facing agent that gathers preferences and orchestrates the planning process (event-driven, no polling)
2. **Weather Analyst** - Analyzes weather patterns and forecasts for potential destinations
3. **Vacation Planner** - Recommends destinations and creates detailed travel plans

## Architecture

```
┌─────────────────────────┐
│  Vacation Coordinator   │ ← User interaction (event-driven)
│    (Entrypoint)         │
└───────┬─────────────────┘
        │
        ├─────────────────┐
        │                 │
        ▼                 ▼
┌──────────────┐   ┌─────────────────┐
│   Weather    │   │    Vacation     │
│   Analyst    │   │    Planner      │
└──────────────┘   └─────────────────┘
```

**Communication Pattern**: Hub-and-spoke (coordinator is the hub)

### How Hub-and-Spoke Works

This workflow demonstrates an **emergent communication pattern** rather than an enforced workflow pattern. Unlike Loom's formal workflow patterns (`debate`, `pipeline`, `swarm`, etc.), hub-and-spoke is not implemented as runtime enforcement. Instead, it emerges naturally through:

**Loom's Tri-Modal Communication System:**
- **Message Queue**: Direct agent-to-agent messaging via `send_message`/`receive_message` tools
- **Broadcast Bus**: Topic-based pub/sub via `publish`/`subscribe` tools (not used in this workflow)
- **Shared Memory**: Shared state via `shared_memory_write`/`shared_memory_read` tools (not used in this workflow)

**Why the Coordinator Acts as Hub:**
1. **Entrypoint designation**: Users interact with the coordinator first
2. **Event-driven architecture**: Coordinator uses event-driven messaging (responses automatically injected, no polling)
3. **System prompt instructions**: Coordinator's prompt tells it to delegate to specialists via `send_message`
4. **Specialist agent prompts**: Weather and planner agents call `receive_message` once, then respond via `send_message`

**Key Architectural Detail - Event-Driven Coordinator:**
The coordinator is fully event-driven. It does NOT call `receive_message` - instead, specialist responses are automatically injected into its conversation. This eliminates polling and ensures instant notification when sub-agents complete their work.

The `communication` field in the workflow YAML is **advisory documentation only** - it communicates intent to humans but is not parsed or enforced by the runtime. This flexibility allows you to implement various communication topologies (hub-and-spoke, peer-to-peer, hierarchical) simply by configuring agent prompts and tool usage.

## Usage

### Starting the Workflow

```bash
# Start the looms server (if not already running)
looms serve

# In another terminal, connect to the coordinator
loom --thread vacation-coordinator
```

### Example Interaction

```
User: I want to plan a beach vacation in March with a budget of $3000

Coordinator: Great! Let me help you plan the perfect beach vacation.
             I'll gather some information and consult with my weather
             and planning specialists...

[Coordinator sends request to vacation-planning-workflow:weather-analyst]
[Coordinator sends request to vacation-planning-workflow:vacation-planner]
[Coordinator tells user it's gathering information]
[System automatically injects specialist responses when ready]

Coordinator: Based on the analysis:

             🌤️ Weather Analysis:
             - Caribbean: 80-85°F, low rainfall
             - Thailand: 85-90°F, dry season
             - Maldives: 85-88°F, perfect conditions

             🏖️ Top Recommendations:
             1. Playa del Carmen, Mexico ($2,800)
             2. Koh Samui, Thailand ($2,950)
             3. Riviera Maya, Mexico ($2,600)

             [Detailed itineraries and breakdown...]
```

## Features

### Coordinator Agent
- **Communication Model**: Event-driven (responses automatically injected, no polling)
- Gathers user preferences (dates, budget, activities, climate)
- Delegates work to specialist agents via `send_message`
- Synthesizes results into comprehensive vacation plans
- Saves final plans to `~/.loom/artifacts/vacation-plan-{timestamp}.md`
- **Memory**: SQLite with conversational profile (max_history: 1000)
- **Config**: max_turns: 100, max_tool_executions: 200, timeout: 600s
- **Tools**: shell_execute, tool_search, send_message (NO receive_message - fully event-driven)

### Weather Analyst
- **Communication Model**: Request-response (calls `receive_message` once, then responds)
- Discovers and uses available weather APIs via `tool_search`
- Provides current conditions and forecasts
- Analyzes historical weather patterns
- Recommends best times to visit destinations
- Saves weather reports to `~/.loom/artifacts/weather-report-{destination}-{timestamp}.json`
- Sends responses to `vacation-planning-workflow` (the workflow name) via `send_message`
- **Memory**: SQLite with data_intensive profile (max_history: 800)
- **Config**: max_turns: 50, max_tool_executions: 150, timeout: 600s
- **Tools**: shell_execute, tool_search, send_message, receive_message

### Vacation Planner
- **Communication Model**: Request-response (calls `receive_message` once, then responds)
- Researches destinations matching user criteria
- Provides budget breakdowns and cost estimates
- Recommends accommodations and activities
- Creates sample itineraries
- Considers visa requirements and safety
- Saves destination reports to `~/.loom/artifacts/destinations-{timestamp}.md`
- **Memory**: SQLite with balanced profile (max_history: 1000)
- **Config**: max_turns: 60, max_tool_executions: 150, timeout: 600s
- **Tools**: shell_execute, tool_search, send_message, receive_message

## Configuration

### Memory Profiles

Each agent uses a different memory compression profile optimized for its workload:

- **Coordinator** (`conversational`): Optimized for back-and-forth conversation with user (1000 messages)
- **Weather Analyst** (`data_intensive`): Handles large weather data responses (800 messages)
- **Vacation Planner** (`balanced`): Mix of research and structured output (1000 messages)

### Tool Discovery

All agents use dynamic tool discovery via `tool_search`:
- Coordinator discovers `send_message` tool
- Weather Analyst discovers web API and web search tools for weather data
- Vacation Planner discovers web search, HTML, and information tools

### Self-Correction and Observability

All agents have:
- **Self-correction**: Enabled for automatic error recovery
- **Observability**: Full tracing and metrics export to Hawk
- **Workflow tags**: All agents tagged with `workflow: vacation-planning`

## Customization

### Adding New Destinations

The vacation-planner agent can be customized to focus on specific regions:

```yaml
# In vacation-planner.yaml, modify system_prompt:
system_prompt: |
  You are a Vacation Planning Expert specializing in European destinations...
```

### Adding Weather Providers

The weather-analyst discovers tools dynamically via `tool_search`. Install weather MCP tools and they will be automatically discovered:

```bash
# Example: Install a weather MCP server
looms config set mcp.servers.weather.command /path/to/weather-mcp
looms config set mcp.servers.weather.env.API_KEY "your-key"
```

### Adjusting Budget Ranges

Modify the vacation-planner system prompt to focus on specific budget tiers:
- Budget: < $1,500
- Mid-range: $1,500 - $4,000
- Luxury: > $4,000

## Output Artifacts

The workflow saves detailed artifacts to `~/.loom/artifacts/`:

- `vacation-plan-{timestamp}.md` - Complete vacation plan (coordinator)
- `weather-report-{destination}-{timestamp}.json` - Weather analysis data (weather-analyst)
- `destinations-{timestamp}.md` - Detailed destination research (vacation-planner)

## Troubleshooting

### No weather data available
- Ensure weather MCP tools are installed
- The weather-analyst will use `tool_search` to discover available tools
- Check discovered tools: `loom --thread weather-analyst` then ask it to list tools

### Specialist agents not responding
- Check that all agents are running in the workflow
- Verify agent IDs in send_message calls: must be `vacation-planning-workflow:weather-analyst` format
- Check looms server logs for message delivery issues

### Destinations seem generic
- Provide more specific preferences to the coordinator
- Specify activities, travel style, or specific regions
- The planner will adapt recommendations based on detail provided

### Workflow timeout
- Adjust timeout in workflow config: `timeout_seconds: 1800` (default: 30 minutes)
- Individual agent timeouts: 600s (10 minutes) per agent

## Dependencies

### Required Tools (built into Loom)

All agents have access to:
- `shell_execute` - Execute shell commands
- `tool_search` - Discover available tools dynamically
- `get_error_detail` - Get detailed error information
- `search_conversation` - Search conversation history
- `recall_conversation` - Recall specific conversation segments
- `clear_recalled_context` - Clear recalled context

### Communication Tools

- **Coordinator**: `send_message` only (event-driven, no receive)
- **Specialists**: `send_message` and `receive_message`
- **Note**: This workflow does NOT use shared_memory or broadcast bus tools

### Optional MCP Tools (discovered via tool_search)

Weather Analyst can discover:
- Weather APIs (OpenWeather, Weather.gov, etc.)
- Climate data services
- Forecast providers

Vacation Planner can discover:
- Maps and location services
- Travel information APIs
- Currency conversion tools
- Hotel/activity booking APIs

## Development

### Testing Individual Agents

```bash
# Test coordinator (event-driven)
loom --thread vacation-coordinator

# Test weather analyst (request-response)
loom --thread weather-analyst

# Test vacation planner (request-response)
loom --thread vacation-planner
```

### Understanding Event-Driven Coordinator

The coordinator is unique - it does NOT poll for messages. When you send it requests:
1. It calls `send_message` to delegate work
2. It tells you it's working on it
3. Responses from specialists are automatically injected into its conversation
4. It sees the responses and synthesizes them for you

This eliminates polling delays and ensures instant coordination.

### Modifying Communication Pattern

The workflow uses hub-and-spoke (coordinator as hub). To implement different patterns, modify agent system prompts to change messaging behavior. The `communication` field in YAML is documentation only:

```yaml
# In vacation-planning-workflow.yaml:
communication:
  pattern: "peer-to-peer"  # Advisory only - implement via agent prompts
```

## Architecture Notes

### Why Event-Driven Coordinator?

Traditional hub-and-spoke patterns require the hub to poll for responses:
```
send_message → poll receive_message → timeout/retry logic
```

This workflow's coordinator is event-driven:
```
send_message → system injects responses automatically → coordinator sees responses
```

Benefits:
- **Zero polling overhead**: No wasted API calls checking for messages
- **Instant notification**: Coordinator sees responses immediately when ready
- **Simpler logic**: No timeout/retry management needed
- **Better UX**: User sees progress updates instead of waiting periods

### Specialist Agent Pattern

Weather and vacation-planner agents follow a simpler request-response pattern:
1. Wait for notification of pending message
2. Call `receive_message` ONCE to get the request
3. Process the request (fetch data, analyze, create artifacts)
4. Call `send_message` to send complete response
5. Wait for next notification

This pattern ensures specialists don't poll and waste resources.

## License

Part of the Loom agent framework examples.
