FROM docker.m.daocloud.io/library/python:3.12-bookworm
WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -i https://pypi.tuna.tsinghua.edu.cn/simple -r requirements.txt
COPY main.py .
EXPOSE 7330
CMD ["uvicorn", "main:app", "--host", "0.0.0.0", "--port", "7330"]
