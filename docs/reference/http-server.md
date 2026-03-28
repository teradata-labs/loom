# HTTP Server Configuration

The Loom HTTP server provides REST API access (via gRPC-gateway), Server-Sent Events (SSE) streaming, CORS support, and Swagger UI documentation.

**Version**: v1.2.0

## Feature Status

- ✅ HTTP/REST gateway for LoomService (via grpc-gateway)
- ✅ SSE streaming for `/v1/weave:stream`
- ✅ CORS middleware with configurable origins, methods, headers
- ✅ Swagger UI served from CDN
- ✅ OpenAPI spec served from `gen/openapiv2/loom/v1/loom.swagger.json`
- ✅ Apps browser (`/apps/`) when AppHTMLProvider is configured
- ✅ Simple health check (`/health`) and detailed health check (`/v1/health`)
- ⚠️ OpenAPI spec is read from disk at runtime (requires `gen/openapiv2/` directory to be present alongside the binary)

## Quick Start

Start the server with HTTP endpoints enabled:

```bash
# Default HTTP port: 5006
looms serve --http-port 5006

# Disable HTTP (gRPC only)
looms serve --http-port 0
```

## Endpoints

### Core Endpoints

- **Simple Health Check**: `GET /health` -- returns `{"status":"healthy"}` (static, always 200 if server is running)
- **gRPC Health Check**: `GET /v1/health` -- returns `HealthStatus` proto with per-component status, version, and uptime (via gRPC-gateway)
- **Swagger UI**: `GET /swagger-ui`
- **OpenAPI Spec**: `GET /openapi.json`
- **SSE Streaming**: `POST /v1/weave:stream`

### API Endpoints

LoomService gRPC endpoints are available via HTTP/REST through the grpc-gateway. Only LoomService is registered with the HTTP gateway (other gRPC services like ToolRegistryService are gRPC-only). Example endpoints:

- `POST /v1/weave` - Execute agent query
- `POST /v1/sessions` - Create session
- `GET /v1/sessions/{session_id}` - Get session
- `POST /v1/weave:stream` - Stream agent execution (SSE)

This is a subset; the LoomService exposes 68 HTTP path patterns via gRPC-gateway. See `/swagger-ui` for the complete API documentation.

### Apps Browser

- **Apps UI**: `GET /apps/` - Browse and interact with Loom apps (available when an app HTML provider is configured)

The `/apps/` endpoint serves the HTML UI for Loom apps. Requests to `/apps` (without trailing slash) are redirected to `/apps/`.

## CORS Configuration

### Development (Default)

By default, CORS is permissive for developer experience:

```yaml
server:
  http_port: 5006
  cors:
    enabled: true
    allowed_origins: ["*"]  # ⚠️ INSECURE for production!
    allowed_methods: ["GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH"]
    allowed_headers: ["*"]
    exposed_headers: ["Content-Length", "Content-Type"]
    allow_credentials: false
    max_age: 86400  # 24 hours
```

**⚠️ Security Warning**: Wildcard origins allow any website to access your API. The server logs a warning on startup.

### Production Configuration

For production deployments, **always specify allowed origins**:

```yaml
server:
  http_port: 5006
  cors:
    enabled: true
    # ✅ Specify exact domains
    allowed_origins:
      - "https://yourdomain.com"
      - "https://app.yourdomain.com"
      - "https://www.yourdomain.com"

    # ✅ Restrict methods to what's needed
    allowed_methods:
      - "GET"
      - "POST"
      - "PUT"
      - "DELETE"
      - "OPTIONS"

    # ✅ Specify required headers only
    allowed_headers:
      - "Content-Type"
      - "Authorization"
      - "X-Request-ID"

    exposed_headers:
      - "Content-Length"
      - "Content-Type"
      - "X-Request-ID"

    # ✅ Enable credentials for authenticated APIs
    allow_credentials: true

    max_age: 86400
```

**Example config**: [`examples/config/looms-production-cors.yaml`](../../examples/config/looms-production-cors.yaml)

### CORS Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `enabled` | bool | `true` | Enable/disable CORS |
| `allowed_origins` | []string | `["*"]` | Allowed origin domains (wildcard or specific) |
| `allowed_methods` | []string | `["GET", "POST", ...]` | Allowed HTTP methods |
| `allowed_headers` | []string | `["*"]` | Allowed request headers |
| `exposed_headers` | []string | `["Content-Length", ...]` | Headers exposed to browser |
| `allow_credentials` | bool | `false` | Allow credentials (cookies, auth headers) |
| `max_age` | int | `86400` | Preflight cache duration (seconds) |

### Security Validations

The server enforces security rules:

1. **Wildcard + Credentials Blocked**: Cannot use `allowed_origins: ["*"]` with `allow_credentials: true` (browsers reject this)
2. **Startup Warnings**: Wildcard origins trigger warnings in logs

### Environment Variables

Override CORS settings via environment variables. Loom uses the `LOOM_` prefix with underscores replacing dots in config keys:

```bash
# Override specific CORS settings via environment
export LOOM_SERVER_CORS_ENABLED=true
export LOOM_SERVER_CORS_ALLOW_CREDENTIALS=false

# Or specify a config file via CLI flag
looms serve --config /path/to/config.yaml

# Default config file location: $LOOM_DATA_DIR/looms.yaml
```

## Swagger UI

### Accessing Documentation

Open your browser to:
```
http://localhost:5006/swagger-ui
```

The Swagger UI provides:
- Interactive API documentation
- Request/response schemas
- "Try it out" functionality
- Complete endpoint listing

### OpenAPI Specification

The raw OpenAPI spec is available at:
```
http://localhost:5006/openapi.json
```

Use this URL for:
- API client generation
- Postman/Insomnia imports
- Contract testing
- Documentation tools

### CDN-Based UI

Swagger UI is loaded from CDN (no bundling required):
- Zero impact on binary size
- Always up-to-date UI
- Requires internet connectivity to load assets

## Testing

### Simple Health Check

```bash
curl http://localhost:5006/health
```

Response:
```json
{"status":"healthy"}
```

### Detailed Health Check (via gRPC-gateway)

```bash
curl http://localhost:5006/v1/health
```

Response (example):
```json
{
  "status": "healthy",
  "components": {
    "llm.default": { "status": "healthy", "latencyMs": "142" }
  },
  "version": "1.2.0",
  "uptimeSeconds": "3600"
}
```

### CORS Preflight

```bash
curl -X OPTIONS http://localhost:5006/v1/weave \
  -H "Origin: https://example.com" \
  -H "Access-Control-Request-Method: POST" \
  -v
```

Expected headers:
```
Access-Control-Allow-Origin: *
Access-Control-Allow-Methods: GET, POST, PUT, DELETE, OPTIONS, PATCH
Access-Control-Allow-Headers: *
Access-Control-Max-Age: 86400
```

### API Call with CORS

```bash
curl -X POST http://localhost:5006/v1/sessions \
  -H "Origin: https://myapp.com" \
  -H "Content-Type: application/json" \
  -d '{}' \
  -v
```

CORS headers will be present in response.

### SSE Streaming

```bash
curl -N -X POST http://localhost:5006/v1/weave:stream \
  -H "Content-Type: application/json" \
  -H "Origin: https://myapp.com" \
  -d '{"query":"test","session_id":""}' \
  -v
```

## Common Issues

### CORS Not Working

**Symptom**: Browser shows CORS errors despite configuration

**Solutions**:
1. Check server logs for CORS warnings
2. Verify `enabled: true` in config
3. Ensure origin matches exactly (including protocol and port)
4. Check browser console for specific error

### Preflight Failures

**Symptom**: OPTIONS requests fail with 404 or 405

**Solutions**:
1. Ensure CORS middleware is enabled
2. Check that `allowed_methods` includes `OPTIONS`
3. Verify server is listening on correct port

### Wildcard + Credentials Error

**Symptom**: Server exits immediately on startup with a fatal log message

**Cause**: Invalid combination of `allowed_origins: ["*"]` with `allow_credentials: true` (the server calls `logger.Fatal` which terminates the process)

**Solution**: Either:
- Use specific origins with credentials: `allowed_origins: ["https://app.com"]` + `allow_credentials: true`
- Or use wildcard without credentials: `allowed_origins: ["*"]` + `allow_credentials: false`

### Swagger UI Not Loading

**Symptom**: `/swagger-ui` page is blank or assets fail to load

**Solutions**:
1. Check `http_port` is configured (not 0); if 0, no HTTP server is started
2. Verify server started successfully (check logs for "Starting HTTP server")
3. Ensure CDN is accessible (Swagger UI CSS/JS are loaded from `cdn.jsdelivr.net`)

### OpenAPI Spec Not Found

**Symptom**: `/openapi.json` returns 404

**Cause**: The spec file is read from disk at `gen/openapiv2/loom/v1/loom.swagger.json` relative to the working directory

**Solution**: Run the server from the project root, or ensure the `gen/openapiv2/` directory is present in the working directory

## Integration Examples

### Frontend (React/Vue/Angular)

```javascript
// Configure API client with CORS
// Note: withCredentials requires the server's allowed_origins to list your
// exact origin (not "*") and allow_credentials: true. The default CORS config
// uses wildcard origins with allow_credentials: false, so omit withCredentials
// unless you have configured specific origins.
const api = axios.create({
  baseURL: 'http://localhost:5006',
  headers: {
    'Content-Type': 'application/json'
  }
});

// Call Loom API
const response = await api.post('/v1/weave', {
  query: 'Analyze sales data',
  session_id: sessionId
});
```

### SSE Streaming Client

The `/v1/weave:stream` endpoint requires `POST`, so the browser `EventSource` API (GET-only) cannot be used. Use `fetch()` with a `ReadableStream` instead:

```javascript
async function streamWeave(query, sessionId) {
  const response = await fetch('http://localhost:5006/v1/weave:stream', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ query, session_id: sessionId }),
  });

  const reader = response.body.getReader();
  const decoder = new TextDecoder();

  while (true) {
    const { done, value } = await reader.read();
    if (done) break;

    const chunk = decoder.decode(value, { stream: true });
    // Each SSE event is separated by a blank line
    for (const line of chunk.split('\n')) {
      if (line.startsWith('data: ')) {
        const progress = JSON.parse(line.slice(6));
        console.log('Progress:', progress);
      }
    }
  }
}

streamWeave('test', '');
```

## Performance

### Caching

- Preflight responses cached for 24 hours (configurable via `max_age`)
- Reduces OPTIONS requests from browsers
- Improves latency for cross-origin requests

### Concurrency

- HTTP server handles concurrent requests via Go's `net/http` goroutine-per-request model
- SSE streaming supports multiple simultaneous connections
- No explicit connection limits (OS defaults apply)
- Server timeouts: `ReadTimeout=30s`, `WriteTimeout=0` (disabled for SSE), `IdleTimeout=120s`

## Security Best Practices

1. **Production Origins**: Always use specific domains in production
2. **HTTPS Only**: Use HTTPS in production (configure TLS)
3. **Credentials**: Only enable if authentication required
4. **Header Restrictions**: Limit `allowed_headers` to necessary headers
5. **Method Restrictions**: Only allow methods your API uses
6. **Regular Updates**: Keep Loom updated for security patches

## Related Documentation

- [TLS Configuration](./tls.md) - HTTPS setup
- [CLI Reference](./cli.md) - Command-line options
- [Production Configuration](../../examples/config/looms-production-cors.yaml) - Example config

## Troubleshooting

Enable debug logging for CORS issues:

```yaml
logging:
  level: "debug"
  format: "json"
```

Check logs for CORS middleware activity.
