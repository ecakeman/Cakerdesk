import httpx
import psycopg


def test_b3_happy_path(stack):
    first = stack.create_run("hello")
    body = stack.wait_run(first)
    assert body["status"] == "succeeded"
    assert body["result"] == {"summary": "hello", "data": {"ok": True}}
    events = httpx.get(
        f"{stack.public}/v1/runs/{first}/events",
        headers={"Authorization": f"Bearer {stack.api_key}"},
        timeout=5,
    )
    events.raise_for_status()
    types = [item["type"] for item in events.json()["items"]]
    assert types == [
        "run.queued",
        "run.started",
        "message.assistant",
        "tool.started",
        "tool.finished",
        "run.succeeded",
    ]
    second = stack.create_run("hello")
    again = stack.wait_run(second)
    assert again["status"] == "succeeded"
    assert again["result"]["summary"] == "hello"


def test_no_result_fails(stack):
    run_id = stack.create_run("text")
    body = stack.wait_run(run_id)
    assert body["status"] == "failed"
    assert body["error"]["code"] == "no_result"
    items = httpx.get(f"{stack.mock}/mock/requests", params={"user": run_id}, timeout=5).json()["items"]
    assert len(items) == 3


def test_checkpoint_written(stack):
    run_id = stack.create_run("hello")
    body = stack.wait_run(run_id)
    assert body["status"] == "succeeded"
    with psycopg.connect(stack.db_admin) as conn:
        row = conn.execute(
            "SELECT 1 FROM lg.checkpoints WHERE thread_id = %s",
            (run_id,),
        ).fetchone()
    assert row is not None


def test_invalid_tool_args(stack):
    run_id = stack.create_run("bad-args")
    body = stack.wait_run(run_id)
    assert body["status"] == "succeeded"
    assert body["result"] == {"summary": "recovered"}
