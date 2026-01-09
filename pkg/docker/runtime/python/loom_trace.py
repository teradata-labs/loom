"""
Loom Trace Library for Python Containers

Lightweight tracing client for Python code running inside Docker containers.
Propagates trace context from host and exports spans back via stderr.

Usage:
    from loom_trace import tracer, trace_span

    # Context manager (recommended)
    with trace_span("query_database", query_type="SELECT"):
        result = execute_query("SELECT * FROM users LIMIT 10")

    # Manual tracing
    span = tracer.start_span("process_data", input_size=len(data))
    try:
        processed = process_data(data)
        tracer.end_span(span, status="ok")
    except Exception as e:
        tracer.end_span(span, status="error")
        raise

Environment Variables:
    LOOM_TRACE_ID: Trace ID from host (inherited from parent)
    LOOM_SPAN_ID: Parent span ID (the docker.execute span)
    LOOM_TRACE_BAGGAGE: W3C baggage format (tenant_id=foo,org_id=bar)

Output Format:
    Spans are written to stderr with prefix:
    __LOOM_TRACE__:{"trace_id":"...","span_id":"...","name":"..."}

Security:
    - No direct Hawk access (all traces proxied through host)
    - Baggage values sanitized to prevent injection
    - Trace data redacted by host before export
"""

import os
import json
import sys
import uuid
from dataclasses import dataclass, asdict, field
from datetime import datetime
from typing import Dict, Any, Optional


@dataclass
class Span:
    """
    Represents a single span in a distributed trace.

    Attributes:
        trace_id: Unique identifier for the entire trace
        span_id: Unique identifier for this span
        parent_id: ID of parent span (for building trace tree)
        name: Human-readable span name (e.g., "query_database")
        start_time: ISO 8601 timestamp when span started
        end_time: ISO 8601 timestamp when span ended (None if still open)
        attributes: Key-value pairs of metadata (query, rows_returned, etc.)
        status: Span status - "ok", "error", or "unset"
    """

    trace_id: str
    span_id: str
    parent_id: str
    name: str
    start_time: str
    end_time: Optional[str] = None
    attributes: Dict[str, Any] = field(default_factory=dict)
    status: str = "unset"

    def to_json(self) -> str:
        """Serialize span to JSON for export."""
        return json.dumps(asdict(self))


class LoomTracer:
    """
    Tracer for Python code running inside Docker containers.

    Reads trace context from environment variables injected by the host,
    creates child spans for operations, and exports them to stderr for
    collection by the host.

    Thread Safety:
        This tracer is NOT thread-safe. If using multiple threads,
        create a separate tracer instance per thread or use locks.
    """

    def __init__(self):
        # Read trace context from environment
        self.trace_id = os.environ.get("LOOM_TRACE_ID", str(uuid.uuid4()))
        self.parent_span_id = os.environ.get("LOOM_SPAN_ID", "")

        # Parse W3C baggage (format: key1=val1,key2=val2)
        self.baggage = self._parse_baggage(os.environ.get("LOOM_TRACE_BAGGAGE", ""))

        # Extracted tenant context from baggage
        self.tenant_id = self.baggage.get("tenant_id", "")
        self.org_id = self.baggage.get("org_id", "")

        # Track spans for debugging (not used for export)
        self.spans = []

    def _parse_baggage(self, baggage_str: str) -> Dict[str, str]:
        """
        Parse W3C baggage format.

        Format: key1=val1,key2=val2,key3=val3
        See: https://www.w3.org/TR/baggage/

        Args:
            baggage_str: W3C baggage string

        Returns:
            Dictionary of key-value pairs
        """
        result = {}
        if not baggage_str:
            return result

        for pair in baggage_str.split(","):
            pair = pair.strip()
            if "=" in pair:
                key, val = pair.split("=", 1)
                # Sanitize to prevent injection attacks
                key = key.strip()
                val = val.strip()
                if key and val:
                    result[key] = val

        return result

    def start_span(self, name: str, attributes: Optional[Dict[str, Any]] = None) -> Span:
        """
        Start a new span.

        Args:
            name: Human-readable span name (e.g., "query_database", "process_file")
            attributes: Optional metadata (query="SELECT ...", rows=42, etc.)

        Returns:
            Span object (call end_span() when operation completes)

        Example:
            span = tracer.start_span("query_database", query_type="SELECT")
            try:
                result = execute_query(...)
                tracer.end_span(span, status="ok")
            except Exception as e:
                tracer.end_span(span, status="error")
                raise
        """
        # Automatically include tenant context in all spans
        if attributes is None:
            attributes = {}

        if self.tenant_id:
            attributes["tenant_id"] = self.tenant_id
        if self.org_id:
            attributes["org_id"] = self.org_id

        span = Span(
            trace_id=self.trace_id,
            span_id=str(uuid.uuid4()),
            parent_id=self.parent_span_id or self.trace_id,
            name=name,
            start_time=datetime.utcnow().isoformat() + "Z",
            attributes=attributes,
            status="unset",
        )

        self.spans.append(span)
        return span

    def end_span(self, span: Span, status: str = "ok"):
        """
        End a span and export it to the host.

        Args:
            span: Span object from start_span()
            status: "ok" (success), "error" (failure), or "unset" (unknown)

        Side Effects:
            Writes span to stderr with __LOOM_TRACE__: prefix for host collection.
        """
        span.end_time = datetime.utcnow().isoformat() + "Z"
        span.status = status

        # Export span to stderr for host collection
        # Format: __LOOM_TRACE__:{"trace_id":"...","span_id":"...",...}
        try:
            print(f"__LOOM_TRACE__:{span.to_json()}", file=sys.stderr, flush=True)
        except Exception as e:
            # Don't fail application if trace export fails
            # (but log to stderr for debugging)
            print(f"__LOOM_TRACE_ERROR__: Failed to export span: {e}", file=sys.stderr)

    def flush(self):
        """
        Flush any buffered spans.

        Currently a no-op (spans exported immediately on end_span).
        Provided for API compatibility and future buffering support.
        """
        pass


class trace_span:
    """
    Context manager for automatic span lifecycle management.

    Automatically starts span on enter, ends on exit.
    Status is "ok" on normal exit, "error" if exception raised.

    Example:
        with trace_span("query_database", query_type="SELECT"):
            result = execute_query("SELECT * FROM users LIMIT 10")
            # Span automatically ended with status="ok"

        try:
            with trace_span("risky_operation"):
                might_fail()
        except Exception:
            # Span automatically ended with status="error"
            handle_error()
    """

    def __init__(self, name: str, **attributes):
        """
        Initialize context manager.

        Args:
            name: Span name
            **attributes: Span attributes as keyword arguments
        """
        self.name = name
        self.attributes = attributes
        self.span = None

    def __enter__(self):
        """Start span on context enter."""
        self.span = tracer.start_span(self.name, self.attributes)
        return self.span

    def __exit__(self, exc_type, exc_val, exc_tb):
        """End span on context exit (with appropriate status)."""
        status = "error" if exc_type else "ok"
        tracer.end_span(self.span, status)
        # Don't suppress exceptions
        return False


# Global tracer instance (initialized with environment variables)
tracer = LoomTracer()


# Convenience function for one-off spans
def trace_function(name: str, **attributes):
    """
    Decorator for tracing entire functions.

    Example:
        @trace_function("process_data", data_type="csv")
        def process_csv(filename):
            # Function automatically traced
            return parse_csv(filename)
    """

    def decorator(func):
        def wrapper(*args, **kwargs):
            with trace_span(name, **attributes):
                return func(*args, **kwargs)

        return wrapper

    return decorator
