FROM python:3.12-bookworm
WORKDIR /app
COPY pyproject.toml uv.lock ./
COPY src ./src
COPY scenarios ./scenarios
RUN pip install --no-cache-dir -i https://pypi.tuna.tsinghua.edu.cn/simple uv && uv sync --frozen --no-dev
EXPOSE 7330
CMD ["uv", "run", "--no-dev", "uvicorn", "mockllm.app:app", "--host", "0.0.0.0", "--port", "7330"]
