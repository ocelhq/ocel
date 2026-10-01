import io
import threading
from wsgiref.simple_server import make_server

from fakebucket import _body, _Quiet, _Threading
from protobuf.wkt import Timestamp

from ocel.gen.app.task.v1.task_connect import TaskServiceWSGIApplication
from ocel.gen.app.task.v1.task_pb import (
    BatchTriggerResponse,
    CancelRunResponse,
    ListRunsResponse,
    ReplayRunResponse,
    RescheduleRunResponse,
    RetrieveRunResponse,
    Run,
    RunStatus,
    TriggerResponse,
)
from ocel.gen.app.topic.v1.topic_connect import TopicServiceWSGIApplication
from ocel.gen.app.topic.v1.topic_pb import (
    CountDeadLettersResponse,
    DeadLetter,
    ListDeadLettersResponse,
    Message,
    PurgeDeadLettersResponse,
    RedriveDeadLettersResponse,
    SendResponse,
)

TOKEN = "letmein"

_AT = Timestamp(seconds=1_700_000_000, nanos=0)


def stored_run(
    id: str,
    task: str = "resize-image",
    payload: bytes = b'{"url":"a.png","width":100}',
    output: bytes = b'{"ok":true}',
    metadata: bytes = b'{"plan":"pro"}',
) -> Run:
    return Run(
        id=id,
        task=task,
        status=RunStatus.COMPLETED,
        payload=payload,
        output=output,
        error="",
        attempts=2,
        tags=["user:1"],
        metadata=metadata,
        created_at=_AT,
        due_at=_AT,
        started_at=_AT,
        finished_at=_AT,
        expires_at=None,
    )


class Tasks:
    def __init__(self, runtime):
        self.runtime = runtime

    def trigger(self, request, ctx):
        self.runtime.seen("Trigger", request, ctx)
        return TriggerResponse(id="run_1")

    def batch_trigger(self, request, ctx):
        self.runtime.seen("BatchTrigger", request, ctx)
        return BatchTriggerResponse(ids=[f"run_{n + 1}" for n in range(len(request.items))])

    def retrieve_run(self, request, ctx):
        self.runtime.seen("RetrieveRun", request, ctx)
        return RetrieveRunResponse(run=stored_run(request.id, **self.runtime.stored_run_json))

    def list_runs(self, request, ctx):
        self.runtime.seen("ListRuns", request, ctx)
        return ListRunsResponse(runs=[stored_run("run_1"), stored_run("run_2")], next_cursor="c2")

    def cancel_run(self, request, ctx):
        self.runtime.seen("CancelRun", request, ctx)
        run = stored_run(request.id)
        run.status = RunStatus.CANCELED
        return CancelRunResponse(run=run)

    def replay_run(self, request, ctx):
        self.runtime.seen("ReplayRun", request, ctx)
        return ReplayRunResponse(id="run_9")

    def reschedule_run(self, request, ctx):
        self.runtime.seen("RescheduleRun", request, ctx)
        run = stored_run(request.id)
        run.status = RunStatus.DELAYED
        run.due_at = request.due_at
        return RescheduleRunResponse(run=run)


class Topics:
    def __init__(self, runtime):
        self.runtime = runtime

    def send(self, request, ctx):
        self.runtime.seen("Send", request, ctx)
        return SendResponse(message_id="01HZY3V0J9Q8C7B6A5Z4Y3X2W1")

    def list_dead_letters(self, request, ctx):
        self.runtime.seen("ListDeadLetters", request, ctx)
        return ListDeadLettersResponse(
            dead_letters=[
                DeadLetter(
                    execution="01HZY3V0J9Q8C7B6A5Z4Y3X2W1-ship",
                    message=Message(id="01HZY3V0J9Q8C7B6A5Z4Y3X2W1", published_at=_AT),
                    payload=self.runtime.dead_letter_payload,
                    attempts=3,
                    error="carrier down",
                    failed_at=_AT,
                )
            ],
            next_cursor="next",
        )

    def redrive_dead_letters(self, request, ctx):
        self.runtime.seen("RedriveDeadLetters", request, ctx)
        return RedriveDeadLettersResponse(redriven=len(request.executions) or 5)

    def purge_dead_letters(self, request, ctx):
        self.runtime.seen("PurgeDeadLetters", request, ctx)
        return PurgeDeadLettersResponse(purged=len(request.executions) or 4)

    def count_dead_letters(self, request, ctx):
        self.runtime.seen("CountDeadLetters", request, ctx)
        return CountDeadLettersResponse(count=3)


class Runtime:
    def __init__(self):
        self.calls: list[tuple[str, object]] = []
        self.authorizations: list[str | None] = []
        self.stored_run_json: dict[str, bytes] = {}
        self.dead_letter_payload = b'{"order":7}'
        self._tasks = TaskServiceWSGIApplication(Tasks(self))
        self._topics = TopicServiceWSGIApplication(Topics(self))
        self.server = make_server(
            "127.0.0.1", 0, self._dispatch, server_class=_Threading, handler_class=_Quiet
        )
        self.url = f"http://127.0.0.1:{self.server.server_address[1]}"
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def seen(self, method, request, ctx):
        self.calls.append((method, request))
        self.authorizations.append(ctx.request_headers.get("Authorization"))

    def requests(self, method):
        return [request for seen, request in self.calls if seen == method]

    def close(self):
        self.server.shutdown()
        self.server.server_close()

    def _dispatch(self, environ, start_response):
        environ["wsgi.input"] = io.BytesIO(_body(environ))
        if environ.get("PATH_INFO", "").startswith("/app.task.v1."):
            return self._tasks(environ, start_response)
        return self._topics(environ, start_response)
