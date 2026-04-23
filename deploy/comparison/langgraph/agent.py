"""Minimal LangGraph agent served via async gRPC for framework throughput comparison.

This implementation is intentionally generous to LangGraph:
- Uses grpcio.aio (best Python async gRPC performance)
- Uses uvloop for faster event loop
- Minimal agent graph (single node, no tools)
- Python 3.12+ for best interpreter performance

The agent accepts a text query, passes it through a single LangGraph node
that calls the mock LLM, and returns the response.
"""

import argparse
import asyncio
import logging
import signal
import uuid
from concurrent import futures

import grpc
from grpc import aio

from langchain_core.messages import HumanMessage
from langgraph.graph import StateGraph, START, END
from typing import TypedDict, Annotated

from mock_llm import MockLLM

# Import generated proto stubs
import service_pb2
import service_pb2_grpc

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(message)s")
logger = logging.getLogger(__name__)


# --- LangGraph Agent Definition ---

class AgentState(TypedDict):
    query: str
    response: str


def create_agent(llm: MockLLM):
    """Create a minimal LangGraph agent with a single LLM call node."""

    async def call_llm(state: AgentState) -> AgentState:
        messages = [HumanMessage(content=state["query"])]
        result = await llm.ainvoke(messages)
        return {"query": state["query"], "response": result.content}

    graph = StateGraph(AgentState)
    graph.add_node("llm", call_llm)
    graph.add_edge(START, "llm")
    graph.add_edge("llm", END)

    return graph.compile()


# --- gRPC Service ---

class BenchServicer(service_pb2_grpc.BenchServiceServicer):
    def __init__(self, agent, llm):
        self.agent = agent
        self.llm = llm

    async def Process(self, request, context):
        session_id = request.session_id or str(uuid.uuid4())

        result = await self.agent.ainvoke(
            {"query": request.query, "response": ""},
        )

        return service_pb2.ProcessResponse(
            response=result["response"],
            session_id=session_id,
        )


async def serve(port: int, latency_ms: float):
    llm = MockLLM(latency_seconds=latency_ms / 1000.0)
    agent = create_agent(llm)

    server = aio.server(
        futures.ThreadPoolExecutor(max_workers=10),
        options=[
            ("grpc.max_send_message_length", 50 * 1024 * 1024),
            ("grpc.max_receive_message_length", 50 * 1024 * 1024),
        ],
    )
    service_pb2_grpc.add_BenchServiceServicer_to_server(
        BenchServicer(agent, llm), server
    )
    server.add_insecure_port(f"[::]:{port}")

    await server.start()
    logger.info(f"LangGraph gRPC server listening on :{port} (llm_latency={latency_ms}ms)")

    # Graceful shutdown
    loop = asyncio.get_event_loop()
    stop = asyncio.Event()

    def signal_handler():
        logger.info("Shutting down...")
        stop.set()

    for sig in (signal.SIGTERM, signal.SIGINT):
        loop.add_signal_handler(sig, signal_handler)

    await stop.wait()
    await server.stop(5)
    logger.info("Server stopped.")


def main():
    parser = argparse.ArgumentParser(description="LangGraph benchmark server")
    parser.add_argument("--port", type=int, default=50051, help="gRPC port")
    parser.add_argument("--llm-latency-ms", type=float, default=1.0, help="Mock LLM latency in ms")
    args = parser.parse_args()

    # Use uvloop if available for better async performance
    try:
        import uvloop
        uvloop.install()
        logger.info("Using uvloop")
    except ImportError:
        logger.info("uvloop not available, using default event loop")

    asyncio.run(serve(args.port, args.llm_latency_ms))


if __name__ == "__main__":
    main()
