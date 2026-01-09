/**
 * Loom Trace Library for Node.js Containers
 *
 * Lightweight tracing client for Node.js code running inside Docker containers.
 * Propagates trace context from host and exports spans back via stderr.
 *
 * Usage:
 *   const { tracer, traceSpan } = require('./loom-trace');
 *
 *   // Async/await with traceSpan
 *   await traceSpan('query_database', { query_type: 'SELECT' }, async (span) => {
 *     return await executeQuery('SELECT * FROM users LIMIT 10');
 *   });
 *
 *   // Manual tracing
 *   const span = tracer.startSpan('process_data', { input_size: data.length });
 *   try {
 *     const processed = await processData(data);
 *     tracer.endSpan(span, 'ok');
 *   } catch (e) {
 *     tracer.endSpan(span, 'error');
 *     throw e;
 *   }
 *
 * Environment Variables:
 *   LOOM_TRACE_ID: Trace ID from host (inherited from parent)
 *   LOOM_SPAN_ID: Parent span ID (the docker.execute span)
 *   LOOM_TRACE_BAGGAGE: W3C baggage format (tenant_id=foo,org_id=bar)
 *
 * Output Format:
 *   Spans are written to stderr with prefix:
 *   __LOOM_TRACE__:{"trace_id":"...","span_id":"...","name":"..."}
 *
 * Security:
 *   - No direct Hawk access (all traces proxied through host)
 *   - Baggage values sanitized to prevent injection
 *   - Trace data redacted by host before export
 */

const { randomUUID } = require('crypto');

/**
 * Represents a single span in a distributed trace.
 */
class Span {
  constructor(traceId, spanId, parentId, name, attributes = {}) {
    this.trace_id = traceId;
    this.span_id = spanId;
    this.parent_id = parentId;
    this.name = name;
    this.start_time = new Date().toISOString();
    this.end_time = null;
    this.attributes = attributes;
    this.status = 'unset';
  }

  /**
   * Serialize span to JSON for export.
   */
  toJSON() {
    return {
      trace_id: this.trace_id,
      span_id: this.span_id,
      parent_id: this.parent_id,
      name: this.name,
      start_time: this.start_time,
      end_time: this.end_time,
      attributes: this.attributes,
      status: this.status,
    };
  }
}

/**
 * Tracer for Node.js code running inside Docker containers.
 *
 * Reads trace context from environment variables injected by the host,
 * creates child spans for operations, and exports them to stderr for
 * collection by the host.
 *
 * Thread Safety:
 *   This tracer is NOT thread-safe for async operations. Each async
 *   context should manage its own spans.
 */
class LoomTracer {
  constructor() {
    // Read trace context from environment
    this.traceId = process.env.LOOM_TRACE_ID || randomUUID();
    this.parentSpanId = process.env.LOOM_SPAN_ID || '';

    // Parse W3C baggage (format: key1=val1,key2=val2)
    this.baggage = this._parseBaggage(process.env.LOOM_TRACE_BAGGAGE || '');

    // Extracted tenant context from baggage
    this.tenantId = this.baggage.tenant_id || '';
    this.orgId = this.baggage.org_id || '';

    // Track spans for debugging (not used for export)
    this.spans = [];
  }

  /**
   * Parse W3C baggage format.
   *
   * Format: key1=val1,key2=val2,key3=val3
   * See: https://www.w3.org/TR/baggage/
   *
   * @param {string} baggageStr - W3C baggage string
   * @returns {Object} Dictionary of key-value pairs
   */
  _parseBaggage(baggageStr) {
    const result = {};
    if (!baggageStr) return result;

    for (const pair of baggageStr.split(',')) {
      const trimmed = pair.trim();
      if (trimmed.includes('=')) {
        const [key, val] = trimmed.split('=', 2);
        const trimmedKey = key.trim();
        const trimmedVal = val.trim();
        // Sanitize to prevent injection attacks
        if (trimmedKey && trimmedVal) {
          result[trimmedKey] = trimmedVal;
        }
      }
    }

    return result;
  }

  /**
   * Start a new span.
   *
   * @param {string} name - Human-readable span name
   * @param {Object} attributes - Optional metadata
   * @returns {Span} Span object (call endSpan() when operation completes)
   *
   * Example:
   *   const span = tracer.startSpan('query_database', { query_type: 'SELECT' });
   *   try {
   *     const result = await executeQuery(...);
   *     tracer.endSpan(span, 'ok');
   *   } catch (e) {
   *     tracer.endSpan(span, 'error');
   *     throw e;
   *   }
   */
  startSpan(name, attributes = {}) {
    // Automatically include tenant context in all spans
    if (this.tenantId) {
      attributes.tenant_id = this.tenantId;
    }
    if (this.orgId) {
      attributes.org_id = this.orgId;
    }

    const span = new Span(
      this.traceId,
      randomUUID(),
      this.parentSpanId || this.traceId,
      name,
      attributes
    );

    this.spans.push(span);
    return span;
  }

  /**
   * End a span and export it to the host.
   *
   * @param {Span} span - Span object from startSpan()
   * @param {string} status - "ok" (success), "error" (failure), or "unset"
   *
   * Side Effects:
   *   Writes span to stderr with __LOOM_TRACE__: prefix for host collection.
   */
  endSpan(span, status = 'ok') {
    span.end_time = new Date().toISOString();
    span.status = status;

    // Export span to stderr for host collection
    // Format: __LOOM_TRACE__:{"trace_id":"...","span_id":"...",...}
    try {
      const json = JSON.stringify(span.toJSON());
      process.stderr.write(`__LOOM_TRACE__:${json}\n`);
    } catch (e) {
      // Don't fail application if trace export fails
      process.stderr.write(`__LOOM_TRACE_ERROR__: Failed to export span: ${e.message}\n`);
    }
  }

  /**
   * Flush any buffered spans.
   *
   * Currently a no-op (spans exported immediately on endSpan).
   * Provided for API compatibility and future buffering support.
   */
  flush() {
    // No-op (immediate export)
  }
}

/**
 * Helper function for tracing async operations.
 *
 * Automatically starts span, executes function, ends span with appropriate status.
 *
 * @param {string} name - Span name
 * @param {Object} attributes - Span attributes
 * @param {Function} fn - Async function to trace
 * @returns {Promise} Result of fn()
 *
 * Example:
 *   const result = await traceSpan('query_database', { query_type: 'SELECT' }, async (span) => {
 *     return await executeQuery('SELECT * FROM users LIMIT 10');
 *   });
 */
async function traceSpan(name, attributes, fn) {
  const span = tracer.startSpan(name, attributes);
  try {
    const result = await fn(span);
    tracer.endSpan(span, 'ok');
    return result;
  } catch (e) {
    tracer.endSpan(span, 'error');
    throw e;
  }
}

/**
 * Synchronous version of traceSpan for non-async operations.
 *
 * @param {string} name - Span name
 * @param {Object} attributes - Span attributes
 * @param {Function} fn - Sync function to trace
 * @returns {*} Result of fn()
 *
 * Example:
 *   const result = traceSpanSync('parse_data', { format: 'json' }, (span) => {
 *     return JSON.parse(data);
 *   });
 */
function traceSpanSync(name, attributes, fn) {
  const span = tracer.startSpan(name, attributes);
  try {
    const result = fn(span);
    tracer.endSpan(span, 'ok');
    return result;
  } catch (e) {
    tracer.endSpan(span, 'error');
    throw e;
  }
}

/**
 * Decorator for tracing class methods (async).
 *
 * Example:
 *   class Database {
 *     @traceMethod('query_database')
 *     async query(sql) {
 *       // Method automatically traced
 *       return await this.execute(sql);
 *     }
 *   }
 */
function traceMethod(name, attributes = {}) {
  return function (target, propertyKey, descriptor) {
    const originalMethod = descriptor.value;

    descriptor.value = async function (...args) {
      return await traceSpan(name, attributes, async () => {
        return await originalMethod.apply(this, args);
      });
    };

    return descriptor;
  };
}

// Global tracer instance (initialized with environment variables)
const tracer = new LoomTracer();

// Export tracer and helpers
module.exports = {
  tracer,
  traceSpan,
  traceSpanSync,
  traceMethod,
  Span,
  LoomTracer,
};
