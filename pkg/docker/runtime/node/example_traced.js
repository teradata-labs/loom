#!/usr/bin/env node
/**
 * Example: Using loom-trace.js for distributed tracing in Docker containers.
 *
 * This script demonstrates how to instrument Node.js code with Loom's
 * container-side tracing library. Spans are automatically collected by
 * the host and exported to Hawk.
 *
 * Environment Variables (automatically injected by Loom):
 *   LOOM_TRACE_ID: Trace ID from host
 *   LOOM_SPAN_ID: Parent span ID (docker.execute)
 *   LOOM_TRACE_BAGGAGE: W3C baggage (tenant_id, org_id)
 *
 * Usage:
 *   # Run in Loom-managed Docker container:
 *   node example_traced.js
 */

const { tracer, traceSpan, traceSpanSync } = require('./loom-trace');

/**
 * Simulate async database query with tracing.
 */
async function queryDatabase(query) {
  return await traceSpan('query_database', { query_type: 'SELECT', query }, async () => {
    // Simulate async query execution
    await new Promise(resolve => setTimeout(resolve, 100));
    return [{ id: 1, name: 'Alice' }, { id: 2, name: 'Bob' }];
  });
}

/**
 * Process query results with tracing.
 */
async function processResults(results) {
  return await traceSpan('process_results', { result_count: results.length }, async () => {
    // Simulate async processing
    const processed = [];
    for (const row of results) {
      await new Promise(resolve => setTimeout(resolve, 50));
      processed.push(row.name.toUpperCase());
    }
    return processed;
  });
}

/**
 * Synchronous operation with tracing.
 */
function parseData(data) {
  return traceSpanSync('parse_data', { format: 'json' }, () => {
    // Synchronous parsing
    return JSON.parse(data);
  });
}

/**
 * Main function with comprehensive tracing examples.
 */
async function main() {
  console.log('Starting traced example...');
  console.log(`Trace ID: ${tracer.traceId}`);
  console.log(`Parent Span ID: ${tracer.parentSpanId}`);
  console.log(`Tenant ID: ${tracer.tenantId}`);
  console.log(`Org ID: ${tracer.orgId}`);
  console.log('');

  // Example 1: Async tracing with traceSpan helper (recommended)
  await traceSpan('main_workflow', {}, async () => {
    const results = await queryDatabase('SELECT * FROM users LIMIT 10');
    const processed = await processResults(results);

    console.log(`Processed ${processed.length} results:`);
    processed.forEach(name => console.log(`  - ${name}`));
  });

  // Example 2: Manual span management for complex control flow
  const span = tracer.startSpan('cleanup', { resource: 'temp_files' });
  try {
    await new Promise(resolve => setTimeout(resolve, 50));
    console.log('\nCleanup completed');
    tracer.endSpan(span, 'ok');
  } catch (e) {
    tracer.endSpan(span, 'error');
    throw e;
  }

  // Example 3: Synchronous operation tracing
  const jsonData = '{"key": "value"}';
  const parsed = parseData(jsonData);
  console.log(`\nParsed data: ${JSON.stringify(parsed)}`);

  // Example 4: Error handling
  try {
    await traceSpan('risky_operation', {}, async () => {
      throw new Error('Simulated error');
    });
  } catch (e) {
    console.log(`\nHandled error: ${e.message}`);
    // Span automatically marked as error
  }

  console.log('\nExample completed! Check Hawk for trace visualization.');
}

// Run main with error handling
main().catch(err => {
  console.error('Fatal error:', err);
  process.exit(1);
});
