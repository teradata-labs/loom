"""Mock LLM for LangGraph benchmarking — equivalent to Loom's loadtest.Provider."""

import asyncio
from langchain_core.language_models import BaseChatModel
from langchain_core.messages import AIMessage, BaseMessage
from langchain_core.outputs import ChatGeneration, ChatResult
from typing import Any, List, Optional


class MockLLM(BaseChatModel):
    """A mock LLM that returns a fixed response after a configurable delay.

    This is intentionally generous to LangGraph — it uses the minimal
    LangChain interface with no extra overhead.
    """

    latency_seconds: float = 0.001  # 1ms default, matching Loom's benchmark
    response_text: str = "This is a mock response for benchmarking purposes."
    model_name: str = "mock-llm-v1"

    @property
    def _llm_type(self) -> str:
        return "mock"

    async def _agenerate(
        self,
        messages: List[BaseMessage],
        stop: Optional[List[str]] = None,
        **kwargs: Any,
    ) -> ChatResult:
        if self.latency_seconds > 0:
            await asyncio.sleep(self.latency_seconds)
        message = AIMessage(content=self.response_text)
        return ChatResult(generations=[ChatGeneration(message=message)])

    def _generate(
        self,
        messages: List[BaseMessage],
        stop: Optional[List[str]] = None,
        **kwargs: Any,
    ) -> ChatResult:
        # Sync fallback — not used in async benchmarks
        import time
        if self.latency_seconds > 0:
            time.sleep(self.latency_seconds)
        message = AIMessage(content=self.response_text)
        return ChatResult(generations=[ChatGeneration(message=message)])
