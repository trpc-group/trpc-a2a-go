"""Official Python SDK client for the Go/Python interoperability example."""

import argparse
import asyncio
import uuid

import httpx
from a2a.client import (
    A2ACardResolver,
    A2AClientError,
    AgentCardResolutionError,
    Client,
    ClientConfig,
    ClientFactory,
)
from a2a.types import (
    AgentCard,
    CancelTaskRequest,
    GetTaskRequest,
    ListTasksRequest,
    Message,
    Part,
    Role,
    SendMessageConfiguration,
    SendMessageRequest,
    SubscribeToTaskRequest,
    Task,
    TaskState,
    StreamResponse,
)
from a2a.utils.errors import TaskNotFoundError

TENANTS = ("tenant-a", "tenant-b")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--agent", default="http://localhost:8080/")
    args = parser.parse_args()
    asyncio.run(run(args.agent))


async def run(agent_url: str) -> None:
    base_url = agent_url.rstrip("/") + "/"
    shared_context_id = str(uuid.uuid4())
    shared_message_id = str(uuid.uuid4())

    async with httpx.AsyncClient(timeout=10) as http:
        streaming_factory = ClientFactory(
            ClientConfig(
                streaming=True,
                httpx_client=http,
                supported_protocol_bindings=["JSONRPC"],
            )
        )
        unary_factory = ClientFactory(
            ClientConfig(
                streaming=False,
                httpx_client=http,
                supported_protocol_bindings=["JSONRPC"],
            )
        )
        clients: dict[str, Client] = {}
        unary_clients: dict[str, Client] = {}
        cards: dict[str, AgentCard] = {}
        for tenant in TENANTS:
            card = await resolve_tenant_card(http, base_url, tenant)
            clients[tenant] = streaming_factory.create(card)
            unary_clients[tenant] = unary_factory.create(card)
            cards[tenant] = card

        print("=== Python client -> A2A server ===")
        print(f"  server          : {base_url}")
        print(f"  shared contextId: {shared_context_id}")
        print(f"  shared messageId: {shared_message_id}\n")

        tasks: dict[str, Task] = {}
        for tenant in TENANTS:
            client = clients[tenant]
            card = cards[tenant]
            print(f"--- {tenant} ---")
            print(f"  name       : {card.name}")
            print(f"  description: {card.description}")
            for iface in card.supported_interfaces:
                print(
                    "  interface  : "
                    f"binding={iface.protocol_binding} "
                    f"url={iface.url} "
                    f"tenant={iface.tenant}"
                )

            print("\n  [send]")
            send_text = f"hello from Python for {tenant}"
            print(f"  input     : {send_text!r}")
            task = await stream_task(
                client,
                SendMessageRequest(
                    message=user_message(
                        send_text,
                        message_id=shared_message_id,
                        context_id=shared_context_id,
                    )
                ),
            )
            if task.status.state != TaskState.TASK_STATE_COMPLETED:
                raise RuntimeError(
                    f"{tenant} task state is {TaskState.Name(task.status.state)}"
                )
            tasks[tenant] = task
            print(f"  completed : task={task.id} result={first_artifact_text(task)!r}")

            print("\n  [input-required]")
            print(f"  input     : {'profile'!r}")
            pending = await stream_task(
                client,
                SendMessageRequest(message=user_message("profile")),
            )
            if pending.status.state != TaskState.TASK_STATE_INPUT_REQUIRED:
                raise RuntimeError(
                    f"{tenant} profile task state is "
                    f"{TaskState.Name(pending.status.state)}"
                )
            print(f"  input     : {'Ada'!r} (continue task={pending.id})")
            completed = await stream_task(
                client,
                SendMessageRequest(
                    message=user_message(
                        "Ada",
                        context_id=pending.context_id,
                        task_id=pending.id,
                    )
                ),
            )
            if (
                completed.id != pending.id
                or completed.status.state != TaskState.TASK_STATE_COMPLETED
            ):
                raise RuntimeError(
                    f"{tenant} continuation did not complete task {pending.id}"
                )
            print(
                f"  continued : task={completed.id} "
                f"result={first_artifact_text(completed)!r}"
            )

            print("\n  [get/list]")
            stored = await client.get_task(
                GetTaskRequest(id=task.id, history_length=10)
            )
            listed = await client.list_tasks(
                ListTasksRequest(
                    context_id=shared_context_id,
                    include_artifacts=True,
                )
            )
            if len(listed.tasks) != 1 or listed.tasks[0].id != task.id:
                raise RuntimeError(
                    f"{tenant} list leaked tenant data: {list(listed.tasks)!r}"
                )
            print(
                f"  get/list  : task={stored.id} history={len(stored.history)} "
                f"scopedTasks={len(listed.tasks)}\n"
            )

        print("--- tenant isolation ---")
        await expect_task_not_found(clients["tenant-b"], tasks["tenant-a"].id)
        print(f"  tenant-b cannot read tenant-a task {tasks['tenant-a'].id}")
        await expect_task_not_found(clients["tenant-a"], tasks["tenant-b"].id)
        print(f"  tenant-a cannot read tenant-b task {tasks['tenant-b'].id}\n")

        print("--- cancellation ---")
        running = await send_async_task(
            unary_clients["tenant-a"],
            SendMessageRequest(
                message=user_message("wait"),
                configuration=SendMessageConfiguration(return_immediately=True),
            ),
        )
        subscription = clients["tenant-a"].subscribe(
            SubscribeToTaskRequest(id=running.id)
        )
        first = await anext(subscription)
        if not first.HasField("task"):
            raise RuntimeError(f"subscription must start with Task, got {first!r}")
        subscription_path = [describe_stream_event(first)]

        await expect_cancel_not_found(unary_clients["tenant-b"], running.id)
        await unary_clients["tenant-a"].cancel_task(
            CancelTaskRequest(id=running.id)
        )
        saw_canceled = False
        async for event in subscription:
            subscription_path.append(describe_stream_event(event))
            if (
                event.HasField("status_update")
                and event.status_update.status.state
                == TaskState.TASK_STATE_CANCELED
            ):
                saw_canceled = True
        if not saw_canceled:
            raise RuntimeError(
                f"subscription for task {running.id} closed without CANCELED"
            )
        canceled = await clients["tenant-a"].get_task(
            GetTaskRequest(id=running.id)
        )
        print(f"  subscription: {' -> '.join(subscription_path)}")
        print(
            f"  tenant-a canceled own task={canceled.id} "
            f"state={short_state(canceled.status.state)}; "
            "tenant-b could not cancel it"
        )

        print("\n=== interoperability and tenant isolation verified ===")


def user_message(
    text: str,
    message_id: str | None = None,
    context_id: str | None = None,
    task_id: str | None = None,
) -> Message:
    return Message(
        role=Role.ROLE_USER,
        message_id=message_id or str(uuid.uuid4()),
        context_id=context_id or str(uuid.uuid4()),
        task_id=task_id,
        parts=[Part(text=text)],
    )


async def resolve_tenant_card(
    http: httpx.AsyncClient,
    base_url: str,
    tenant: str,
) -> AgentCard:
    """Resolve either the Python path card or the Go query card."""
    path_resolver = A2ACardResolver(http, f"{base_url}{tenant}")
    try:
        card = await path_resolver.get_agent_card()
    except AgentCardResolutionError as error:
        if error.status_code not in (400, 404):
            raise
        query_resolver = A2ACardResolver(http, base_url)
        card = await query_resolver.get_agent_card(
            http_kwargs={"params": {"tenant": tenant}}
        )
    return card


async def send_async_task(client: Client, request: SendMessageRequest) -> Task:
    events = [event async for event in client.send_message(request)]
    if len(events) != 1 or not events[0].HasField("task"):
        raise RuntimeError(f"expected one task response, got {events!r}")
    return events[0].task


async def stream_task(client: Client, request: SendMessageRequest) -> Task:
    path: list[str] = []
    task_id = ""
    async for event in client.send_message(request):
        if event.HasField("task"):
            task_id = event.task.id
        elif event.HasField("status_update"):
            task_id = event.status_update.task_id
        elif event.HasField("artifact_update"):
            task_id = event.artifact_update.task_id
        path.append(describe_stream_event(event))
    if not task_id:
        raise RuntimeError("stream closed without a task ID")
    print(f"  stream    : {' -> '.join(path)}")
    return await client.get_task(GetTaskRequest(id=task_id, history_length=10))


def describe_stream_event(event: StreamResponse) -> str:
    if event.HasField("task"):
        return f"Task({short_state(event.task.status.state)})"
    if event.HasField("status_update"):
        return f"Status({short_state(event.status_update.status.state)})"
    if event.HasField("artifact_update"):
        return f"Artifact({event.artifact_update.artifact.artifact_id})"
    if event.HasField("message"):
        return "Message"
    return "Unknown"


def short_state(state: TaskState) -> str:
    return TaskState.Name(state).removeprefix("TASK_STATE_")


def first_artifact_text(task: Task) -> str:
    if not task.artifacts:
        return ""
    return next((part.text for part in task.artifacts[0].parts if part.text), "")


async def expect_task_not_found(client: Client, task_id: str) -> None:
    try:
        await client.get_task(GetTaskRequest(id=task_id))
    except TaskNotFoundError:
        return
    except A2AClientError as error:
        if is_wrapped_http_404(error):
            return
        raise
    except httpx.HTTPStatusError as error:
        # The Go JSON-RPC server maps TaskNotFound to HTTP 404, which is raised
        # by the official Python transport before it decodes the JSON-RPC body.
        if error.response.status_code == 404:
            return
        raise
    raise RuntimeError(f"cross-tenant task {task_id} was visible")


async def expect_cancel_not_found(client: Client, task_id: str) -> None:
    try:
        await client.cancel_task(CancelTaskRequest(id=task_id))
    except TaskNotFoundError:
        return
    except A2AClientError as error:
        if is_wrapped_http_404(error):
            return
        raise
    except httpx.HTTPStatusError as error:
        if error.response.status_code == 404:
            return
        raise
    raise RuntimeError(f"cross-tenant task {task_id} was cancelable")


def is_wrapped_http_404(error: A2AClientError) -> bool:
    cause = error.__cause__
    return (
        isinstance(cause, httpx.HTTPStatusError) and cause.response.status_code == 404
    )


if __name__ == "__main__":
    main()
