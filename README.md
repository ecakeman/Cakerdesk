Cakerdesk 是可靠的长任务 Agent Runtime：进程被杀掉、抖动、工具做到一半或模型乱调工具之后，租约和事件仍能续跑，不重复副作用，不越权，并且能从事件和数据库重放。

## 启动

```bash
make up
make run-api
curl -s localhost:7310/healthz
```

`make test` 跑 Go 与 kernel 测试。`make lint` 含格式、`lint-stubs` 和 archtest。`make down` 停掉 PostgreSQL 与 Redis。

## 进度

- [x] A1 仓库与本地基础设施
- [ ] A2 数据库与 Agent 版本
- [ ] A3 Mock LLM
- [ ] B1 Session / Run 入队
- [ ] B2 Claim
- [ ] B3 LangGraph kernel
- [ ] B4 事件与 SSE
- [ ] C1 租约与心跳
- [ ] C2 Reaper 与恢复
- [ ] C3 Checkpoint 围栏
- [ ] C4 取消与超时
- [ ] D1 Gateway 与文件工具
- [ ] D2 执行语义
- [ ] D3 sandboxd
- [ ] D4 bash 与崩溃恢复
- [ ] E1 Skill 权限
- [ ] E2 重试、预算与循环防护
- [ ] E3 Compaction
- [ ] F1 破坏性测试、演示与文档
