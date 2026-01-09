#!/usr/bin/env python3
"""
Example: Using loom_trace.py for distributed tracing in Docker containers.

This script demonstrates how to instrument Python code with Loom's
container-side tracing library. Spans are automatically collected by
the host and exported to Hawk.

Environment Variables (automatically injected by Loom):
    LOOM_TRACE_ID: Trace ID from host
    LOOM_SPAN_ID: Parent span ID (docker.execute)
    LOOM_TRACE_BAGGAGE: W3C baggage (tenant_id, org_id)

Usage:
    # Run in Loom-managed Docker container:
    python example_traced.py
"""

import time
import sys

# Import Loom trace library (installed in container)
from loom_trace import tracer, trace_span


def query_database(query):
    """Simulate database query with tracing."""
    with trace_span("query_database", query_type="SELECT", query=query):
        # Simulate query execution
        time.sleep(0.1)
        return [{"id": 1, "name": "Alice"}, {"id": 2, "name": "Bob"}]


def process_results(results):
    """Process query results with tracing."""
    with trace_span("process_results", result_count=len(results)):
        # Simulate processing
        processed = []
        for row in results:
            time.sleep(0.05)
            processed.append(row["name"].upper())
        return processed


def main():
    """Main function with comprehensive tracing."""
    print("Starting traced example...")
    print(f"Trace ID: {tracer.trace_id}")
    print(f"Parent Span ID: {tracer.parent_span_id}")
    print(f"Tenant ID: {tracer.tenant_id}")
    print(f"Org ID: {tracer.org_id}")
    print("")

    # Example 1: Context manager tracing (recommended)
    with trace_span("main_workflow"):
        results = query_database("SELECT * FROM users LIMIT 10")
        processed = process_results(results)

        print(f"Processed {len(processed)} results:")
        for name in processed:
            print(f"  - {name}")

    # Example 2: Manual span management
    span = tracer.start_span("cleanup", resource="temp_files")
    try:
        time.sleep(0.05)
        print("\nCleanup completed")
        tracer.end_span(span, status="ok")
    except Exception as e:
        tracer.end_span(span, status="error")
        raise

    # Example 3: Error handling
    try:
        with trace_span("risky_operation"):
            raise ValueError("Simulated error")
    except ValueError as e:
        print(f"\nHandled error: {e}")
        # Span automatically marked as error

    print("\nExample completed! Check Hawk for trace visualization.")


if __name__ == "__main__":
    main()
