# Cakerdesk

Cakerdesk 是一个 Long-Horizon Agent Runtime 展品。一次任务要读资料、写文件、把局部工作交给 Subagent、在自称完成之后按磁盘证据验收、失败后改计划再继续，并把经验留给同一 Project 的下一次 Run。

```text
> 根据 work/sales.csv 和 work/notes.txt 写周销售报告
plan.updated s1 读取 notes.txt pending
tool.completed read_file work/notes.txt
verification.completed 未通过 结论
replan.started verification_failed
plan.updated s4 补写结论 pending
verification.completed 通过
memory.written lesson 缺标题会被退回
run.completed 任务完成 artifacts/report.md
>
```

模型说出「写好了」不会结束。只有 `submit_for_verification` 离开 Lead 循环，Verifier 再读磁盘。失败把对应步骤从 completed 改回 blocked，Replan 保留已完成步骤并改后续计划。

```text
Terminal Agent Shell
        |
        | 自然语言 / 斜杠命令
        v
   Go 产品状态
 Project Thread Run Event
        |
        | HTTP 202
        v
 Python Agent Runtime
 Contract Plan Lead Verify Replan Memory
        |
        v
 Workspace 上的真实文件
```

Shell 只负责输入和事件投影。它不保存 AgentState，也不决定下一步。

## 启动

PostgreSQL 需要自己准备。`make setup` 不安装数据库。

```bash
cp .env.example .env
make setup
make dev
```

`make dev` 在 `:8080` 启动 Go，在 `:8090` 启动 Python，然后进入 Shell。Ctrl-C 会停掉这两个进程。已经启动服务时，用 `make shell` 只进入 Shell。

管理命令仍在，用来查看和调试：`cakerdesk project`、`thread`、`run`、`artifact`、`memory`。HTTP 服务是 `cakerdesk serve`。

## 范围

做到主线真实、模块连通、Demo 能跑完、Shell 能看清过程。不做 Web，不做队列、supervisor、多 Agent 平台。行为细节在 [docs/agent-runtime-design.md](docs/agent-runtime-design.md)。
