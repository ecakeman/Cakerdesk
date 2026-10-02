import os
import signal
import socket
import subprocess
import time
import uuid
from pathlib import Path

import httpx
import psycopg
import pytest

ROOT = Path(__file__).resolve().parents[2]


def _port() -> int:
    sock = socket.socket()
    sock.bind(("127.0.0.1", 0))
    port = sock.getsockname()[1]
    sock.close()
    return port


def _wait_ok(url: str, proc: subprocess.Popen, log_path: Path) -> None:
    deadline = time.time() + 40
    while time.time() < deadline:
        if proc.poll() is not None:
            raise RuntimeError(log_path.read_text()[-4000:])
        try:
            if httpx.get(url, timeout=1).status_code == 200:
                return
        except httpx.HTTPError:
            pass
        time.sleep(0.2)
    raise RuntimeError(log_path.read_text()[-4000:])


class Stack:
    def __init__(self, public: str, api_key: str, db_admin: str, mock: str) -> None:
        self.public = public
        self.api_key = api_key
        self.db_admin = db_admin
        self.mock = mock

    def create_run(self, scenario: str) -> str:
        headers = {"Authorization": f"Bearer {self.api_key}"}
        agent = httpx.post(
            f"{self.public}/v1/agents",
            headers=headers,
            json={"name": "e2e-" + uuid.uuid4().hex[:8]},
            timeout=10,
        )
        agent.raise_for_status()
        agent_id = agent.json()["id"]
        published = httpx.post(
            f"{self.public}/v1/agents/{agent_id}/versions",
            headers=headers,
            json={"config": {"model": "mock-1", "system_prompt": "p", "tools": ["submit_result"]}},
            timeout=10,
        )
        published.raise_for_status()
        session = httpx.post(
            f"{self.public}/v1/sessions",
            headers=headers,
            json={"agent_id": agent_id},
            timeout=10,
        )
        session.raise_for_status()
        run = httpx.post(
            f"{self.public}/v1/sessions/{session.json()['id']}/runs",
            headers=headers,
            json={"input": f"[[scenario:{scenario}]] hi"},
            timeout=10,
        )
        run.raise_for_status()
        return run.json()["id"]

    def wait_run(self, run_id: str) -> dict:
        headers = {"Authorization": f"Bearer {self.api_key}"}
        deadline = time.time() + 10
        last = ""
        while time.time() < deadline:
            res = httpx.get(f"{self.public}/v1/runs/{run_id}", headers=headers, timeout=5)
            res.raise_for_status()
            body = res.json()
            last = body["status"]
            if last in ("succeeded", "failed", "cancelled"):
                return body
            time.sleep(0.1)
        raise AssertionError(f"status {last}")


@pytest.fixture(scope="session")
def stack():
    admin = os.environ.get(
        "CD_TEST_DATABASE_URL",
        "postgres://postgres:postgres@127.0.0.1:7340/postgres?sslmode=disable",
    )
    name = "cd_e2e_" + uuid.uuid4().hex[:8]
    with psycopg.connect(admin, autocommit=True) as conn:
        conn.execute(f"CREATE DATABASE {name} OWNER cd_migrate")
        conn.execute(f"GRANT CONNECT ON DATABASE {name} TO cd_app")
        conn.execute(f"GRANT CONNECT ON DATABASE {name} TO cd_kernel")

    def role_url(user: str) -> str:
        return f"postgres://{user}:{user}@127.0.0.1:7340/{name}?sslmode=disable"

    api_key = "e2e-key"
    internal = "e2e-internal"
    public_port = _port()
    internal_port = _port()
    mock_port = _port()
    base_env = os.environ.copy()
    base_env.update(
        {
            "CD_DATABASE_URL": role_url("cd_app"),
            "CD_MIGRATE_DATABASE_URL": role_url("cd_migrate"),
            "CD_API_KEY": api_key,
            "CD_INTERNAL_TOKEN": internal,
            "CD_KERNEL_SETUP_DATABASE_URL": role_url("cd_migrate"),
            "CD_KERNEL_DATABASE_URL": role_url("cd_kernel"),
            "CD_KERNEL_API_URL": f"http://127.0.0.1:{internal_port}",
            "CD_LLM_BASE_URL": f"http://127.0.0.1:{mock_port}/v1",
            "CD_LLM_API_KEY": "mock",
            "CD_PUBLIC_ADDR": f"127.0.0.1:{public_port}",
            "CD_INTERNAL_ADDR": f"127.0.0.1:{internal_port}",
            "CD_WORKSPACES_DIR": "/tmp/cakerdesk-e2e-ws",
        }
    )
    subprocess.run(
        ["go", "run", "./cmd/cakerdesk", "migrate", "up"],
        cwd=ROOT / "go",
        env=base_env,
        check=True,
    )
    subprocess.run(
        ["uv", "run", "cd-kernel", "setup"],
        cwd=ROOT / "kernel",
        env=base_env,
        check=True,
    )
    logs = []
    procs = []

    def spawn(args: list[str], cwd: Path, name: str) -> tuple[subprocess.Popen, Path]:
        path = Path(f"/tmp/cakerdesk-e2e-{name}.log")
        handle = path.open("w")
        logs.append(handle)
        proc = subprocess.Popen(
            args,
            cwd=cwd,
            env=base_env,
            stdout=handle,
            stderr=subprocess.STDOUT,
            start_new_session=True,
        )
        procs.append(proc)
        return proc, path

    mock, mock_log = spawn(
        ["uv", "run", "uvicorn", "mockllm.app:app", "--host", "127.0.0.1", "--port", str(mock_port)],
        ROOT / "mockllm",
        "mock",
    )
    _wait_ok(f"http://127.0.0.1:{mock_port}/healthz", mock, mock_log)
    api, api_log = spawn(["go", "run", "./cmd/cakerdesk", "-role=api"], ROOT / "go", "api")
    _wait_ok(f"http://127.0.0.1:{public_port}/healthz", api, api_log)
    spawn(["uv", "run", "cd-kernel"], ROOT / "kernel", "kernel")
    holder = Stack(
        f"http://127.0.0.1:{public_port}",
        api_key,
        role_url("cd_migrate"),
        f"http://127.0.0.1:{mock_port}",
    )
    try:
        yield holder
    finally:
        for proc in procs:
            if proc.poll() is None:
                os.killpg(proc.pid, signal.SIGTERM)
        for handle in logs:
            handle.close()
        with psycopg.connect(admin, autocommit=True) as conn:
            conn.execute(
                "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = %s AND pid <> pg_backend_pid()",
                (name,),
            )
            conn.execute(f"DROP DATABASE {name}")
