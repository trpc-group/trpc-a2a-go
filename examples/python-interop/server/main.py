"""Official Python SDK server for the Go/Python interoperability example."""

import argparse
import asyncio
from collections.abc import AsyncGenerator

import uvicorn
from a2a.server.agent_execution import AgentExecutor, RequestContext
from a2a.server.context import ServerCallContext
from a2a.server.events import Event, EventQueue
from a2a.server.request_handlers import DefaultRequestHandler
from a2a.server.routes import create_agent_card_routes, create_jsonrpc_routes
from a2a.server.tasks import InMemoryTaskStore, TaskUpdater
from a2a.types import (
    AgentCapabilities,
    AgentCard,
    AgentInterface,
    AgentSkill,
    CancelTaskRequest,
    Part,
    SendMessageRequest,
    Task,
    TaskState,
    TaskStatus,
)
from a2a.utils.errors import InvalidParamsError, TaskNotFoundError
from starlette.applications import Starlette

TENANTS = ("tenant-a", "tenant-b")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--host", default="localhost")
    parser.add_argument("--port", default=8080, type=int)
    args = parser.parse_args()
    uvicorn.run(create_app(args.host, args.port), host=args.host, port=args.port)


class TenantExecutor(AgentExecutor):
    """Implements the same task lifecycle as the Go basic example."""

    def __init__(self) -> None:
        self._waiting: dict[tuple[str, str], asyncio.Event] = {}

    async def execute(self, context: RequestContext, event_queue: EventQueue) -> None:
        if context.tenant not in TENANTS:
            raise InvalidParamsError(message=f"unknown tenant {context.tenant!r}")
        if not context.message or not context.task_id or not context.context_id:
            raise InvalidParamsError(message="message and task context required")

        updater = TaskUpdater(
            event_queue=event_queue,
            task_id=context.task_id,
            context_id=context.context_id,
        )

        if context.current_task is not None:
            result = (
                f"[{context.tenant}] Hello, {context.get_user_input()}. "
                f"Task {context.task_id} is complete."
            )
            await updater.start_work()
            await updater.add_artifact(
                parts=[Part(text=result)],
                name="profile",
                last_chunk=True,
            )
            await updater.complete(updater.new_agent_message(parts=[Part(text=result)]))
            return

        await event_queue.enqueue_event(
            Task(
                id=context.task_id,
                context_id=context.context_id,
                status=TaskStatus(state=TaskState.TASK_STATE_SUBMITTED),
                history=[context.message],
            )
        )

        text = context.get_user_input().strip()
        if text == "profile":
            await updater.requires_input(
                updater.new_agent_message(
                    parts=[Part(text=f"[{context.tenant}] What name should I use?")]
                )
            )
            return
        if text == "wait":
            await self._wait_for_cancel(context, updater)
            return

        result = f"[{context.tenant}] {text.upper()}"
        await updater.start_work()
        await updater.add_artifact(
            parts=[Part(text=result)],
            name="result",
            last_chunk=True,
        )
        await updater.complete(updater.new_agent_message(parts=[Part(text=result)]))

    async def _wait_for_cancel(
        self, context: RequestContext, updater: TaskUpdater
    ) -> None:
        key = (context.tenant, context.task_id or "")
        waiter = asyncio.Event()
        self._waiting[key] = waiter
        await updater.start_work(
            updater.new_agent_message(
                parts=[Part(text=f"[{context.tenant}] Waiting for cancellation")]
            )
        )
        try:
            await asyncio.wait_for(waiter.wait(), timeout=30)
        except asyncio.TimeoutError:
            result = f"[{context.tenant}] Wait finished"
            await updater.add_artifact(
                parts=[Part(text=result)],
                name="wait",
                last_chunk=True,
            )
            await updater.complete(updater.new_agent_message(parts=[Part(text=result)]))
        finally:
            self._waiting.pop(key, None)

    async def cancel(self, context: RequestContext, event_queue: EventQueue) -> None:
        if not context.task_id or not context.context_id:
            raise InvalidParamsError(message="task context required")
        updater = TaskUpdater(
            event_queue=event_queue,
            task_id=context.task_id,
            context_id=context.context_id,
        )
        await updater.cancel(
            updater.new_agent_message(parts=[Part(text=f"[{context.tenant}] Canceled")])
        )
        waiter = self._waiting.get((context.tenant, context.task_id))
        if waiter is not None:
            waiter.set()


class TenantRequestHandler(DefaultRequestHandler):
    """Closes the active-task cancellation lookup over tenant ownership."""

    async def on_message_send_stream(
        self,
        params: SendMessageRequest,
        context: ServerCallContext,
    ) -> AsyncGenerator[Event, None]:
        if params.message.task_id:
            task = await self.task_store.get(params.message.task_id, context)
            if task is None:
                raise TaskNotFoundError
            yield task
        async for event in super().on_message_send_stream(params, context):
            yield event

    async def on_cancel_task(
        self,
        params: CancelTaskRequest,
        context: ServerCallContext,
    ) -> Task | None:
        if await self.task_store.get(params.id, context) is None:
            raise TaskNotFoundError
        return await super().on_cancel_task(params, context)


def tenant_card(name: str, tenant: str, endpoint: str) -> AgentCard:
    description = f"A2A task lifecycle agent for {tenant}"
    return AgentCard(
        name=name,
        description=description,
        supported_interfaces=[
            AgentInterface(
                url=endpoint,
                protocol_binding="JSONRPC",
                protocol_version="1.0",
                tenant=tenant,
            )
        ],
        version="1.0.0",
        capabilities=AgentCapabilities(
            streaming=True,
            push_notifications=False,
        ),
        default_input_modes=["text/plain"],
        default_output_modes=["text/plain"],
        skills=[
            AgentSkill(
                id=f"task-lifecycle-{tenant}",
                name="Task lifecycle",
                description=description,
                tags=["tasks", "tenant", "interop"],
                examples=["hello", "profile", "wait"],
            )
        ],
    )


def create_app(host: str, port: int) -> Starlette:
    endpoint = f"http://{host}:{port}/"
    cards = {
        "tenant-a": tenant_card("Python Tenant A Agent", "tenant-a", endpoint),
        "tenant-b": tenant_card("Python Tenant B Agent", "tenant-b", endpoint),
    }
    handler = TenantRequestHandler(
        agent_executor=TenantExecutor(),
        # The official store defaults to user scope. Resolve ownership from
        # tenant here so GetTask/ListTasks/CancelTask are actually isolated.
        task_store=InMemoryTaskStore(owner_resolver=lambda context: context.tenant),
        agent_card=cards["tenant-a"],
    )
    routes = [
        *create_agent_card_routes(
            cards["tenant-a"],
            card_url="/tenant-a/.well-known/agent-card.json",
        ),
        *create_agent_card_routes(
            cards["tenant-b"],
            card_url="/tenant-b/.well-known/agent-card.json",
        ),
        *create_jsonrpc_routes(handler, rpc_url="/"),
    ]
    return Starlette(routes=routes)

if __name__ == "__main__":
    main()
