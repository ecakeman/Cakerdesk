Cakerdesk 把一次 LLM Agent 的执行放进数据库驱动的租约里。worker 被杀掉、抖动、工具做到一半或模型乱调工具之后，Run 仍能续跑，已完成的副作用不重复，越权被拒绝，全过程能从事件和数据库复盘。

实现规格是仓库根目录的 `design.md`。

## 启动

```bash
make up
make run-api
curl -s localhost:7310/healthz
```

`make test` 跑 Go 与 kernel 测试。`make lint` 含格式和 archtest。`make down` 停掉 PostgreSQL 与 Redis。步骤收口时再跑 `make lint-stubs`。

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
