# Cakerdesk Agent Runtime 设计

本文是后续实现的施工图，不是待办大全。架构按本文落地，不另起一套结构。实现时按第 25 章收敛：主线行为已经能跑、能影响下一步、能在演示里看见，就停。不要因为 DeerFlow 还有更多字段、middleware 或抽象就继续补。

代码按本文收敛。目录是 `/home/sancho/projects/cakerdesk-next`。本地旧 Cakerdesk 实验仓不作为依据。

参照实现是 [bytedance/deer-flow](https://github.com/bytedance/deer-flow) 的 `backend/packages/harness/deerflow/`，阅读范围包括 Lead Agent、`ThreadState`、`DurableContextMiddleware`、Summarization、Memory、Subagent Executor、Workspace 和相关 middleware。

---

## 1. 项目定位

Cakerdesk 是一个用来展示 Long-Horizon Agent Runtime 的技术展品，不是生产级 Agent 平台。

一次 Run 要能持续做完一件多步任务：读资料、调用工具、把局部工作交给 Subagent、写出文件、在自称完成之后被独立检查、检查失败后改计划再继续、把值得留下的经验写入项目记忆，并在下一次 Run 的模型输入里重新出现。CLI 要能看出这条过程，而不是只看到最后一句回答。

优先级：

```text
核心行为真实
>
模块之间真正连通
>
一条完整展示主线跑通
>
展示层能看出 Agent 在持续工作
>
工程完整度
>
平台化能力
```

职责切分：

- Go 保存产品状态：Project、Thread、Message、Run、Event。Artifact 的正文在磁盘上，Run 只留路径快照。
- Python 做 Agent 决策和执行：LangGraph、Checkpoint、Lead、Context、Memory、Plan、Verification、Replan、Subagent、Reflection。
- CLI 只消费 Go 的产品和事件，投影成输出。

技术选型已经定下：LangGraph、PostgreSQL Checkpoint、PostgreSQL Project Memory、Go 与 Python 之间的 HTTP、本地 Workspace。不做向量检索。

---

## 2. DeerFlow 参照与取舍

DeerFlow 的 Lead 是 `langchain.agents.create_agent` 包出来的工具循环。`ThreadState` 把消息、goal、todos、artifacts、`summary_text`、delegations 放进 Checkpoint。`DurableContextMiddleware.wrap_model_call` 在 `_inject` 里用 `request.override(messages=...)` 把摘要、委派记录、skill 引用插进**这一次**模型请求，并打上不展示给 UI 的标记。这是「持久事实」和「当次模型输入」分开的直接证据。

同一套代码里，Summarization 做的是另一件事：超限后把旧消息从 state 里换掉，并留下 `summary_text`。Memory 则在 Run 之后异步抽出 facts，默认写入 JSON 文件，下一轮通过 `format_memory_for_injection` 放进 system prompt。Subagent 用独立 executor 跑自己的消息，只把结果写回父级 tool result。Workspace 是线程目录下的 uploads / workspace / outputs，工具直接读写文件。

DeerFlow 的完成判断不能照搬。`create_agent` 在模型不再发出 tool call 时结束内循环。官方 Lead 文档也把「模型给出最终消息」当成任务结束。`runtime/goal.py` 再用一个小模型看对话，决定要不要偷偷再跑一轮。这和本展品的闸门相反：普通文本不是完成，完成必须经过 `submit_for_verification`，再由运行时按文件证据判断。

`todos` 只是 plan mode 下的清单，没有「验证失败后保留已完成步骤并改计划」这条边。因此 Plan / Verify / Replan 是 Cakerdesk 图上的节点，不是 middleware。

| DeerFlow 机制 | Cakerdesk 是否保留 | Cakerdesk 做法 | 为什么这样改 |
| --- | --- | --- | --- |
| `create_agent` 工具循环：模型 → 工具 → 观察 → 模型 | 改造 | 自写同等结构的 LeadLoop：model 节点 + tool 节点。无 tool call 不结束 Run | DeerFlow 把无工具的 assistant 文本当成循环结束。本展品只有 `submit_for_verification` 能把控制权交给 Verify |
| `ThreadState` + PostgreSQL checkpointer | 照搬核心机制 | Checkpoint 保存消息、contract、plan、summary、findings、artifacts、delegations、guard | 长任务的事实必须能在进程重启后继续。字段按本展品的闭环裁过，不搬 sandbox / viewed_images / promoted tools |
| `DurableContextMiddleware.wrap_model_call` 只改当次请求 | 照搬核心机制 | `ContextMiddleware` 调用 `ContextManager`，`request.override` 出 `messages_for_llm`，不返回 messages 的 state 更新 | 这是 DeerFlow 里已经验证过的边界。把拼好的上下文写回 `state.messages` 会进 Checkpoint 并逐轮重复 |
| Summarization：摘要 + 删除旧消息 | 照搬核心机制 | 超过阈值时写 `state.summary`，并从 `messages` 删除已被摘要覆盖的旧消息 | 只赋值 summary、不动 messages，历史仍会无限增长 |
| Memory：抽取 facts，下一轮注入 system | 改造 | Reflection 在 Run 终态写入 LangGraph `PostgresStore`，命名空间 `("project", project_id, "memory")`。种类只有 `fact` 和 `lesson`。下次按 project 取最近 N 条，由 Context 注入 | DeerFlow 默认是 `memory.json`，还有工具式 CRUD、置信度、向量无关但很重的队列。展品要的是跨 Run 闭环，存储用已定的 PostgreSQL，不自建第二套记忆表 |
| `runtime/goal.py` 用小模型看对话决定是否再跑 | 删除 | 完成闸门是 submit + Verifier | 目标是否达到由契约和文件证据决定，不由模型自述决定 |
| Plan mode 的 `todos` | 改造 | `plan.steps` 加 status；失败后 `Replan` 节点写 PlanDiff | 清单本身不能表达「保留完成步骤、重开失败步骤」 |
| Subagent executor，结果回父级 tool message | 缩减 | 一个通用 executor。独立 messages、独立模型调用。不写 Memory，不跑 Verify，不能再 delegate。`max_concurrent = 1`，单 Run 最多 6 | 保留隔离和回收。去掉 general-purpose / bash 等角色、验收清单框架和子代理自己的摘要进父记忆 |
| uploads / workspace / outputs | 改造 | `uploads/`、`work/`、`artifacts/`。工具读写真实路径，Verifier 再 `stat` 磁盘 | 目录名按展品约定。不引入 sandbox provider |
| Title、Clarification、ViewImage、Sandbox、Uploads middleware | 删除 | 无 | 不服务这条长任务主线 |
| Skill 发现与正文注入 | 只留痕迹 | 启动时若 `skills/*/SKILL.md` 存在，Context 放名称清单。没有 skill 正文，也没有 skill 执行器 | 证明运行时留了扩展位置 |
| MCP、Gateway、渠道、Extension、Provider 工厂、自定义 Agent | 删除 | 无 | 平台能力。模型用一个 OpenAI 兼容的 ChatModel 配置 |
| Delta checkpoint、tool artifact handle 注册表、PII 脱敏 | 删除 | 普通 full checkpoint；artifact 就是工作区相对路径 | 这些解决的是大规模生产和安全平台问题 |

---

## 3. 展示主线

演示任务叫「周销售报告」。一个 Project 里先跑 Run A，再跑 Run B。Run B 用来证明 Memory 进了下一次模型输入。

开始前，Go 已经为该 Thread 准备好工作区，并写入两份资料：

`workspace/threads/{thread_id}/work/notes.txt`

```text
请根据 sales.csv 写报告。
报告文件必须是 artifacts/report.md。
必须包含三个二级标题：数据摘要、异常点、结论。
异常点不要和全文写作混在同一步里完成。
```

`workspace/threads/{thread_id}/work/sales.csv`

```text
week,units
1,100
2,98
3,110
4,12
5,105
```

用户通过 CLI 提交：

```text
根据 work/notes.txt 和 work/sales.csv，完成 artifacts/report.md。
```

Run A 的过程：

1. Go 创建 Run，调用 Python。Python 发出 `run.started`。
2. EnsureContract 读用户目标和 `notes.txt` 的硬性要求，写出契约：交付物是 `artifacts/report.md`，三个标题都要在文件里出现。发出的事件里不单列 contract 类型；契约体现在随后的 `plan.updated` 和验证标准里。契约本身留在 AgentState。
3. EnsurePlan 写出初始计划并发 `plan.updated`。
   - S1 阅读 notes.txt
   - S2 阅读 sales.csv
   - S3 把异常点分析交给 Subagent
   - S4 撰写 report.md
4. Lead 调用 `read_file` 读两份资料。每次工具前后发 `tool.started` / `tool.completed`。`set_step_status` 把 S1、S2 标成 completed，S3 标成 in_progress，并再发 `plan.updated`。
5. Lead 调用 `delegate_task`，任务是「只根据 sales.csv 解释第 4 周为什么异常，返回一段可放进报告的异常点说明」。Python 发 `subagent.started`。
6. Subagent 在独立消息里自己再读 csv，给出「第 4 周 units=12，相对前后周约 100 属于断点」一类说明。父 Checkpoint 不保存这些内部消息。Lead 只收到一条 ToolMessage。发 `subagent.completed`。S3 标 completed。
7. Lead 写出 `artifacts/report.md`，但这一版只有「数据摘要」和「异常点」，没有「结论」。`write_file` 成功后把相对路径记入 `artifacts`。S4 被标成 completed。这是 Agent 对步骤的看法，不是任务通过。
8. 模型如果先输出「报告写好了」而不调用工具，LeadLoop 不退出。系统规则要求它要么继续干活，要么调用 `submit_for_verification`。
9. Lead 调用 `submit_for_verification`。LeadLoop 结束，控制权到 Verify。
10. Verify 打开磁盘上的 `artifacts/report.md`。文件存在且非空，但缺少标题「结论」。结果 FAIL。写 `findings`，发 `verification.completed`。
11. Verify 已把 S4 从 completed 改成 blocked。图进入 Replan，发 `replan.started`。S1、S2、S3 保持 completed。S4 从 blocked 改成 pending，标题改为补写结论。新增 S5「对照三个标题后再提交」。发新的 `plan.updated`。这次失败写入 `attempts`，供最后的 Reflect 使用。
12. Lead 再次运行。这一轮 Context 同时有新 Plan 和 FAIL findings。它 `read_file` 现有报告，`write_file` 补上「结论」，再 `submit_for_verification`。
13. Verify 看到三个标题都在，且文件非空。PASS。
14. Reflection 写出一条 fact 和一条 lesson，写入该 project 的记忆项，发 `memory.written`。Run 发 `run.completed`，payload 里带 `artifacts/report.md`。

Run B，同一 Project，可以是新 Thread：

用户说：「再写一份简短周报说明，沿用上次的报告要求。」

EnsureContract / EnsurePlan 之后，第一次模型调用的 Context 含 Run A 的 lesson：提交前要对照三个标题，缺「结论」会被退回。这是 Memory 闭环的验收点。Run B 不需要再演一遍失败。

这条主线同时用到 Lead 多轮、工具、Context 变化、Plan、Verification、Replan、Subagent、Artifact、Memory 和 Event。下面各章都回到这条链上，不另起业务。

---

## 4. 总体架构

```text
CLI
  │  JSON 与 SSE
  ▼
Go
  │  产品：Project Thread Message Run Event
  │  POST /internal/runs → 202 ──────────► Python Agent Runtime
  │  POST /internal/runs/{id}/resume → 202
  │  ◄──── POST /internal/runs/{id}/events
  ▼
PostgreSQL
  ├── Go：projects threads messages runs events
  └── Python：LangGraph PostgresSaver 的 checkpoint 表，PostgresStore 的记忆项

本地磁盘
  workspace/threads/{thread_id}/uploads|work|artifacts
```

一次用户发送的路径：

1. CLI 把目标 POST 给 Go。Go 写入 user message 和 Run（status=`running`），创建工作区目录。
2. Go 调用 Python `POST /internal/runs`。Python 校验参数后立刻返回 `202 Accepted`，body 为 `{ "run_id", "status": "accepted" }`。这次 HTTP 到此结束。Python 启动成功只表示 Run 已被接受，不表示 Run 完成。
3. Python 在该请求之外执行图，过程中把事件 POST 回 Go。完成、失败、取消都由这条 Runtime 生命周期负责，不绑在启动请求上。
4. Go 按 `(run_id, seq)` 插入 `events`，SSE 推给 CLI。
5. `run watch` 先 GET 已有事件，再从 `after_seq` 接 SSE。

Python 不对外暴露给浏览器。Go 不调用模型，不改 Plan 内容，不判断报告是否合格。

---

## 5. Run 生命周期

图的边：

```text
START
 → EnsureContract
 → EnsurePlan
 → LeadLoop
      ├─ 普通工具 → 回到 LeadLoop
      ├─ delegate_task → Subagent → ToolMessage → 回到 LeadLoop
      ├─ 普通 assistant 文本 → 回到 LeadLoop
      └─ submit_for_verification → Verify
            ├─ PASS → Reflect → END
            └─ FAIL → Replan → LeadLoop
```

Replan 只有 Verify FAIL 这一条入边。Lead 不能主动请求重规划。

取消在任意模型调用边界生效。`cancel_requested` 的持久事实是 Go 的 `runs.status`。Python 进程内标记只负责当前这次执行立刻停下，不是取消状态的唯一来源。进程重启后，Python 仍从该 Run 的状态判断取消已经提出，然后发 `run.cancelled`，不跑 Reflection。Contract 或 Plan 初始化失败直接 `run.failed`，也不跑 Reflection。Lead 已经执行过工具、委派、验证或重规划之后的运行时失败，先 Reflect 只写 lesson，再 `run.failed`。

| 节点 | 输入 | 核心逻辑 | State 修改 | 输出 / 下一节点 |
| --- | --- | --- | --- | --- |
| 启动（Go 调用 Python，图尚未进节点） | `project_id`、`thread_id`、`run_id`、`goal`、`workspace_root` | 绑定 thread checkpoint，`durability="sync"`。该 Thread 上第一次进入，或上一次图已经结束：清空本 Run 的 `findings` 和 `attempts`，`guard` 计数归零，`artifacts` 以磁盘扫描为准。`resume` 发现图还停在中间节点时，不重新进入本行，直接从 Checkpoint 的下一节点继续 | `guard.run_id`、`guard.*` 计数、`findings=[]`、`artifacts` | 事件 `run.started`。下一节点 EnsureContract。恢复路径不重发本行 |
| EnsureContract | 用户 goal，以及 `work/notes.txt` 若存在则读取其文本 | 一次结构化模型调用，抽出交付物路径和可机器检查的条目。失败则 `run.failed`，不进入 Lead，不 Reflect | 覆盖 `contract` | 无单独事件。下一节点 EnsurePlan |
| EnsurePlan | `contract` | 一次结构化模型调用，产出 2 到 6 步。每步有稳定 `id`。不执行工具。失败则 `run.failed`，不 Reflect | 覆盖 `plan`，`plan.version=1` | 事件 `plan.updated`。下一节点 LeadLoop |
| LeadLoop | 当前 AgentState，外加 Context 临时拼出的模型输入 | 见第 7 章。只在出现 `submit_for_verification` 时离开 | 追加真实的 user/ai/tool 消息；工具成功写盘后更新 `artifacts`；`set_step_status` 只能把步骤改成 `in_progress` 或 `completed` | 工具与步骤事件。submit 则下一节点 Verify；否则留在 LeadLoop |
| Subagent（由 `delegate_task` 在 LeadLoop 内同步调用） | 父级传入的 task 文本、workspace 根、只读的 contract 摘要 | 独立消息、独立模型循环，只用文件工具。见第 12 章 | 父状态只增加一条 ToolMessage，以及 `delegations` 的短记录。子消息不写入父 checkpoint | `subagent.started` / `subagent.completed`。回到 LeadLoop |
| Verify | `contract`、磁盘文件、本 Run 工具结果摘要、必要时最近消息 | 先查文件，再查工具结果，再查消息，最后才允许模型做语义判断。见第 11 章。FAIL 时把对应该交付物的 `completed` 步骤改成 `blocked` | 覆盖 `findings`。追加一条 `attempts` 验证记录。通过时不改步骤状态 | 事件 `verification.completed`。PASS → Reflect；FAIL → Replan |
| Replan | 当前 `plan`、`findings`、`contract`、`attempts` | 结构化模型调用产出新 plan 和 diff。completed 步骤的 id 与 title 必须保留。见第 10 章 | 覆盖 `plan`，`version+1`，写入 `plan.diff`。追加一条 `attempts` 重规划记录 | 事件 `replan.started`（reason 只有 `verification_failed`），随后 `plan.updated`。`guard.replan_count+1`。下一节点 LeadLoop。超过上限则先 Reflect lesson 再 `run.failed` |
| Reflect | 终态 State、`attempts`、交付路径。不只有最终 findings | 一次结构化模型调用，最多 3 条 `fact`、2 条 `lesson`。PASS 可以写 fact 和 lesson。已执行后的运行时失败只写 lesson。初始化失败和取消不进入本节点 | 不改 Checkpoint 里的业务字段。写入 `PostgresStore` | 每条记忆发 `memory.written`。下一节点结束或 `run.failed` |
| 结束 | Reflect 的写入结果，或失败/取消原因 | 图在后台任务里结束，并发出终态事件。Go 根据事件把 Run status 写成终态。启动那次 HTTP 早已返回 | Checkpoint 停在终态，供同一 thread 的下一次 Run 接着读消息和 summary | `run.completed` 或 `run.failed` 或 `run.cancelled` |

启动请求不等图。Go 创建 Run 后向 Python 要一个 202，然后靠 Event 观察。没有任务队列。Python 进程崩溃时，Go 上的 Run 停留在 `running`，Checkpoint 停在最后一个已提交的节点。`resume` 用同一个 `thread_id` 从那里继续，不另写节点调度器。取消请求把 status 写成 `cancel_requested`，恢复时先看这个状态。超时只是展示用的安全网，默认 30 分钟，不是调度框架。

---

## 6. AgentState

Checkpoint 的 thread id 就是产品里的 `thread_id`。一次 Thread 上的多次 Run 共用这条 Checkpoint。`run_id` 放在 `guard` 里，用来区分本 Run 的计数和事件，不另开一条 Checkpoint。

```text
State = 持久事实
messages_for_llm = 当前这一次模型调用的输入
```

下列内容禁止成为 State 字段，也禁止被写进 `messages`：`messages_for_llm`、拼好的 system context、从数据库读出的 memory 投影、任何只为这一轮存在的 context 块。

| 字段 | 是什么 | 谁写 | 谁读 | 生命周期 | 进 Checkpoint | 更新方式 |
| --- | --- | --- | --- | --- | --- | --- |
| `messages` | 用户消息、模型消息、工具结果。不含 Context 拼装块 | LeadLoop、Subagent 的结果回收、Run 开始时追加的那条用户 goal | Context 取最近窗口；Summarization 删除旧段；Reflect 只读不改 | 随 Thread 保留 | 是 | LangGraph `add_messages`。摘要时用删除旧消息的更新，不是另附一份上下文 |
| `contract` | 本 Run 的目标、交付物、可检查条目 | EnsureContract 覆盖写。Run 开始时重置 | EnsurePlan、Context、Verify、Replan、Reflect | 本 Run。下一 Run 覆盖 | 是 | 整块替换 |
| `plan` | 步骤列表、version、最近一次 diff | EnsurePlan 创建；Lead 的 `set_step_status` 只改 status/note；Replan 整块替换并写 diff | Context、Lead、CLI 经由 `plan.updated` | 本 Run。下一 Run 由 EnsurePlan 覆盖 | 是 | 步骤状态是原地改；Replan 是整份替换 |
| `summary` | 已被移出 `messages` 的旧对话压缩文本 | SummarizationMiddleware | Context | 随 Thread 累积，每次压缩覆盖为「旧 summary + 新被裁掉的那段」 | 是 | 替换字符串 |
| `findings` | 最近一次 Verify 的条目列表 | Verify 覆盖。Run 开始清空 | Context、Replan | 本 Run | 是 | 整块替换 |
| `artifacts` | 相对工作区的路径列表，例如 `artifacts/report.md` | Run 开始时扫描 `artifacts/`；`write_file` 成功后把路径并入 | Verify 用它做候选，但仍以磁盘为准；Context 只给路径 | 随 Thread。磁盘文件还在，列表就还在 | 是 | 并集去重 |
| `delegations` | 子任务 id、任务一句话、状态、结果摘要。没有子消息 | `delegate_task` | Context、Lead 下一轮 | 随 Thread 追加，最多保留 20 条 | 是 | 按 `subagent_id` 更新同一条 |
| `attempts` | 本 Run 的验证和重规划短记录：`kind`（verification / replan）、`attempt`、`passed`、`reason`、`plan_version` | Verify 和 Replan 追加。Run 开始清空 | Reflect、Context | 本 Run。不是事件溯源，只留这几条 | 是 | 追加，最多 12 条 |
| `guard` | `run_id`、replan 次数、连续无工具文本次数、重复工具签名、token 累计、`executed`（Lead 是否已跑过工具或委派）、取消标记 | 图的运行时在对应边界更新 | LeadLoop 路由器、TokenBudget、Replan 入口、失败时是否 Reflect | 计数在每次 Run 开始清零；结构一直在 | 是 | 数值替换 |

`contract` 的形状：

```text
goal: string
deliverables:
  - path: string                 # 相对 thread 工作区，必须落在 artifacts/
    must_exist: true
    must_be_nonempty: true
    must_contain: [string]       # 文件正文必须出现的子串，演示里是三个标题
```

`plan` 的形状：

```text
version: int
goal: string
steps:
  - id: string                   # s1、s2，Replan 不得改已完成步骤的 id
    title: string
    status: pending | in_progress | completed | blocked
    note: string
diff: null | { preserved: [id], modified: [id], added: [id], removed: [id] }
```

`findings` 的形状：

```text
passed: bool
items:
  - criterion: string
    status: passed | failed
    evidence: string             # 短句，含相对路径或「文件不存在」
    reason: string
```

步骤 status 不是任务是否完成的真相。`completed` 只表示 Lead 认为这个动作已经做完，不表示 Contract 已满足。因此这条路径合法：`in_progress → completed → Verify FAIL → blocked`。`completed → blocked` 只能由 Verification 失败写入，而且必须有对应失败证据。Lead 的 `set_step_status` 不能把 `completed` 改成 `blocked`，也不能直接写入 `blocked`。Replan 再把 `blocked` 改成 `pending`。任务是否结束只看 Verify 的 `passed` 和随后的 `run.completed`。

---

## 7. Lead Agent Loop

不使用 `create_agent` 作为 Run 的完成闸门，也不改用 `ToolNode`。

DeerFlow 使用 `create_agent`，是因为那个产品接受「模型不再调工具即本轮代理结束」。Cakerdesk 的展示点是运行时不相信这句话。若把 `create_agent` 套在外面，它会在普通文本处返回，外层还要再把同一个代理叫起来，退出条件仍然藏在库里面。LeadLoop 仍是显式的两个节点。真实模型是 `ChatOpenAI`：Lead 用代码里的 `@tool` 做 `bind_tools`，Contract、Plan、Replan、Reflect 用 Pydantic schema 做 `with_structured_output`。测试替身返回已经符合这些形状的对象或 Tool Call，不另做一套模型适配。AIMessage 上的 `tool_calls` 由现有 tool 节点执行，ToolMessage 追加进 `messages`。

```text
Model
 ↓
响应里有没有 tool_calls？
 ├─ 有，且全部是普通工具或 delegate_task
 │    → 执行 → ToolMessage 写入 messages → 回到 Model
 ├─ 有，且包含 submit_for_verification
 │    → 不执行业务副作用 → 离开 LeadLoop → Verify
 └─ 没有
      → 这条 assistant 文本写入 messages
      → guard.plain_text_streak += 1
      → 回到 Model
      → 不进入 Verify
```

普通文本不代表完成，因为模型经常会在证据还没落盘时生成一句总结。演示里第 8 步就是这个情况：CLI 已经打出「报告写好了」，Run 状态仍是 running，直到 submit 之后才出现 Verification。

`submit_for_verification` 的参数只有一个短 `summary`，给事件和 Reflect 用，不作为通过依据。工具实现不写文件、不改 plan。图的条件边看到这个 tool name 就离开循环。它不会先被执行成一条普通观察再碰运气。若同一次响应里既有 `write_file` 又有 submit，先执行写文件，再离开。若同一次里还有别的未执行工具，也先执行那些工具，最后才离开。submit 本身不产生 ToolMessage 以外的业务状态；可以记一条内容为 `submitted` 的 ToolMessage，便于消息历史读得通，但这条消息不是通过证明。

没有 `request_replan`。重规划只有 Verify FAIL 之后才会发生。

工具进入 `messages` 的方式：

1. 模型响应作为 AIMessage 追加，保留 `tool_calls`。
2. 每个已执行工具追加对应 `tool_call_id` 的 ToolMessage。内容是短结果：读文件时是正文，但单次返回截断到 8000 字符，全文仍在磁盘；写文件时是「已写入相对路径」；列目录是文件名。
3. 这些消息是事实，进 Checkpoint。
4. 下一轮 Model 之前，ContextMiddleware 用 State 重新拼请求。拼装块不追加进第 2 步的消息。

Lead 可用工具：`list_dir`、`read_file`、`write_file`、`set_step_status`、`delegate_task`、`submit_for_verification`。没有 shell，没有网络搜索。演示任务不需要它们。

`set_step_status` 只能把已有 step 从 `pending` 或 `in_progress` 改成 `in_progress` 或 `completed`，并可写 note。拒绝 `blocked`，也拒绝改动已经是 `completed` 的步骤。不能新增或删除步骤。新增、删除，以及 `blocked → pending`，只发生在 Replan。

连续无工具文本达到 3 次，Run `failed`，原因 `plain_text_loop`。相同工具名加相同参数的签名连续出现 3 次，Run `failed`，原因 `repeated_tool_call`。

---

## 8. Context Engineering

Context 回答的是：这次模型调用应该看见什么。它不是 State 的一个字段。

### 来源

每次调用 `ContextManager.build(state, memory_rows)` 读取：

- Contract
- 当前 Plan，含最近一次 diff
- Findings，没有则省略
- Project Memory 行，调用方已经按 project 取好
- Summary
- 最近消息
- 这些消息里尚未被 summary 吃掉的 tool / subagent 结果
- Artifact 相对路径

Subagent 返回之后不需要一种新的上下文类型。结果已经是父 `messages` 里的 ToolMessage，下一轮重建时自然出现在最近消息里。Plan 更新、Verify FAIL、Summary 更新也一样：它们先写进 State，下一次模型调用重建时读到。

重建时机就是每一次模型调用，包括 EnsureContract、EnsurePlan、Lead、Replan、Verify 的语义兜底、Reflect、Summarize。业务节点不缓存上一轮的 `messages_for_llm`。

### 优先级和预算

用字符数除以 4 估算 token，不引入分词器。这是展品里的稳定规则，不是账单精度。

总预算 `CONTEXT_TOKEN_BUDGET = 12000`，只约束拼出来的上下文，不含工具 JSON schema。

各层上限：

| 层 | 上限（估算 token） | 裁剪 |
| --- | --- | --- |
| 固定规则（如何使用工具、何谓 submit） | 800 | 不裁 |
| Contract | 800 | 不裁。超长则 EnsureContract 写失败，不在这里截断目标 |
| Plan | 800 | 不裁当前 version 的步骤。diff 可截断到 200 token |
| Findings | 600 | 不裁最近一次。没有则占 0 |
| Memory | 800 | 先丢更旧的行，保留最新。fact 和 lesson 一视同仁，只按时间 |
| Summary | 1500 | 从尾部保留。更旧的摘要内容在下次压缩时被重新概括 |
| Artifact 路径 | 300 | 只保留路径，从不放文件正文 |
| 最近消息，含工具结果 | 剩余额度 | 从最旧的一条开始丢，但至少保留最新 4 条。单条工具结果在进消息时已经截断到 8000 字符 |

裁剪顺序：最近消息里最旧的 → diff 说明 → memory 最旧行 → summary 的头部。Contract、当前步骤列表、最近一次 findings 不参与裁剪。若剩余额度仍不够最近 4 条消息，保留这 4 条并允许这一次略超预算，同时 `guard.token_overshoot` 加一。累计模型调用的真实 usage 另由 TokenBudgetMiddleware 记账，那是停机条件，不是这次拼装的裁剪。

### 边界

```text
AgentState + 该 project 的记忆项
        ↓
ContextManager.build
        ↓
messages_for_llm
        ↓
ContextMiddleware.wrap_model_call
        ↓
request.override(messages=...)
        ↓
Model
```

`wrap_model_call` 的返回值是模型响应。Middleware 不返回 `{messages: ...}`。因此 Checkpoint 里的 `messages` 在这次调用前后只多了模型自己的输出，不会多出 Contract/Plan 拼装块。

拼装后的请求形如：

```text
System: 固定规则
System: Contract / Plan / Findings / Memory / Summary / Artifact 路径
然后才是最近 messages
```

第二段 System 每次整段替换，不追加到历史。

### 同一次演示里的两次模型视野

第一次 Lead 模型调用，State 里已有 contract 和 version 1 的 plan，没有 findings，没有 summary，memory 为空。模型看见：

```text
System 规则：
未调用 submit_for_verification 不算完成。
普通总结文字之后你仍要继续使用工具或提交验证。

System 上下文：
Contract
  goal: 根据资料完成 artifacts/report.md
  deliverable: artifacts/report.md 必须非空
  must_contain: 数据摘要、异常点、结论
Plan v1
  S1 pending 阅读 notes.txt
  S2 pending 阅读 sales.csv
  S3 pending 委派异常点分析
  S4 pending 撰写 report.md
Findings: （无）
Memory: （无）
Summary: （无）
Artifacts: （无）

Human: 根据 work/notes.txt 和 work/sales.csv，完成 artifacts/report.md。
```

Verify FAIL 且 Replan 之后，下一次 Lead 模型调用看见的是另一份上下文。最近消息里还有第一次 submit 之前的工具结果，但决定行为的是新的 Plan 和 Findings：

```text
System 上下文：
Contract
  （同上，未改）
Plan v2
  S1 completed 阅读 notes.txt
  S2 completed 阅读 sales.csv
  S3 completed 委派异常点分析
  S4 blocked 撰写 report.md — 缺少标题「结论」
  S5 pending 补写结论并保留前两节
  diff: preserved s1,s2,s3; modified s4; added s5
Findings
  passed: false
  criterion: report.md 包含「结论」
  evidence: artifacts/report.md 存在且非空，正文没有「结论」
Memory: （仍无，Reflect 还没跑）
Summary: （若此时还没触发压缩则为空）
Artifacts: artifacts/report.md

随后是本 Thread 的最近消息，其中包含 read_file 的观察、delegate 的 ToolMessage、write_file 已写入的短结果。
```

这两次的差别来自 State，不是来自一份写死的长 prompt。第一次没有 Findings，Plan 全是 pending。第二次 Plan 的 version、status 和 findings 都变了，所以模型会去补文件，而不是从头再读一遍资料当成主任务。

---

## 9. Memory / Reflection

```text
Run 到达终态
 ↓
Reflect 决定有没有值得留下的 fact / lesson
 ↓
PostgresStore 中该 project 的记忆项
 ↓
以后任意同 Project 的 Run
 ↓
read_for_context
 ↓
ContextManager 的 Memory 层
 ↓
该 Run 的模型输入
```

Memory 是跨 Run 的项目经验。Context 是这一次调用的工作记忆。Thread 上的对话继续留在 Checkpoint 的 `messages` 和 `summary` 里，不复制进记忆项。

### 写什么

- `fact`：以后还成立的项目事实。演示里可以是「周报交付物路径是 artifacts/report.md，必须含数据摘要、异常点、结论」。
- `lesson`：这次 Run 付出代价才知道的做法。演示里应当是「第一次提交时报告缺了结论，验证失败；补写后再提交才通过」。

不写用户原文的逐字副本，不写工具输出全文，不写计划的每一步。

### 谁写、何时写

只有 Reflect 写。Lead 没有 memory 工具，避免模型在循环中途把未经验证的句子写进长期记忆。

Reflect 的输入是终态 State，外加本 Run 的 `attempts`，外加交付路径。最终 findings 可能已经是 PASS，最终 Plan 也可能已经换成补写步骤。`attempts` 仍保留第一次失败的原因和随后的重规划，所以模型能写出「先缺结论，补写后通过」，而不是只看见最后一次成功。这不是事件溯源，只是本 Run 里最多 12 条短记录。

| Run 结果 | 是否 Reflect |
| --- | --- |
| Contract 初始化失败 | 否，直接 `run.failed` |
| Plan 初始化失败 | 否，直接 `run.failed` |
| Lead 已执行工具、委派、验证或重规划之后的运行时失败 | 是，只写 lesson，然后 `run.failed` |
| Verify 失败后又 PASS | 是，fact 和 lesson |
| 正常 PASS | 是，fact 和 lesson |
| Cancel | 否 |

lesson 必须来自 `attempts` 和消息里的真实过程。演示文案不能写死在代码里。

### 存在哪里、怎么读

记忆放在 LangGraph `PostgresStore`，不另建 `project_memory` 表，也不做向量索引。命名空间是 `("project", project_id, "memory")`。每一项的值是：

```text
kind          fact | lesson
content       纯文本，单条最多 500 字
source_run_id
created_at
```

`read_for_context(project_id)` 取 `created_at` 最新的 20 条，再按时间从旧到新交给 Context。没有关键词检索，没有向量，没有跨项目。Context 再按第 8 章的 800 token 从最旧开始丢。表由 Store 的 `setup()` 创建，不写进 Go 的 migration。

### 跨 Run 时序

```text
Run A
  Verify PASS
  Reflect 插入 lesson「缺结论会被退回」
  memory.written
  run.completed

Run B（同一 project_id，新的 thread 也可以）
  EnsureContract / EnsurePlan
  第一次 Lead 模型调用
  ContextManager 读到这 20 条里的该 lesson
  messages_for_llm 的 Memory 段出现这句话
  Checkpoint 的 messages 里仍然没有这段投影
```

验收时打印或断言的是模型请求，不是 CLI 上的某一句话。CLI 只看到一条短的 `memory.written`，看不到记忆全文。`memory list` 是事后查看，不参与决策。

---

## 10. Planning / Replanning

只有 Plan、PlanStep、Replan、PlanDiff。没有依赖图、优先级、多层计划和独立 Planner 代理。

EnsurePlan 和 Replan 都是一次结构化模型调用，不调用业务工具。它们写出的步骤必须能被演示任务执行：阅读、委派、写文件、补写。步数 2 到 6。

步骤状态：

- `pending`：还没开始。
- `in_progress`：Lead 通过 `set_step_status` 标明正在做。同一时刻允许多个 in_progress，不做成调度器。
- `completed`：Lead 认为这一步的动作已经做完。不自动等于契约满足。
- `blocked`：Verify 已经证明这一步对应的交付不满足契约。只有 Verify 能从 `completed` 写成这个状态。

Replan 只在 Verify 返回 FAIL 时触发。不在每一轮模型调用后自动重规划，Lead 也不能调用工具要求重规划。

Replan 的硬规则，写在给模型的结构化说明里，并由代码再检查一遍：

- status 为 completed 的步骤：id 和 title 原样保留，放进 `diff.preserved`。代码若发现它们被改掉，拒绝这份新计划，让 Replan 再调用一次模型。第二次仍非法则 `run.failed`，原因 `invalid_replan`。
- status 为 blocked 的步骤：必须重估。可以改 title 和 note，id 保留，放进 `diff.modified`，新 status 为 `pending`。不允许把 blocked 直接标成 completed。
- 尚未完成但也不再需要的 pending 步骤可以删除，放进 `diff.removed`。
- 新工作用新 id 追加，放进 `diff.added`，status 为 `pending`。

演示里的 PlanDiff：

```text
原 Plan v1，提交前 Lead 已把前四步都标成 completed
S1 completed 阅读 notes.txt
S2 completed 阅读 sales.csv
S3 completed 委派异常点分析
S4 completed 撰写 report.md

Verify FAIL，evidence 为缺少「结论」
Replan 先把 S4 视为必须重估的步骤（代码在调用模型前把「被失败证据直接否定的交付步骤」标成 blocked）

新 Plan v2
S1 completed 阅读 notes.txt          preserved
S2 completed 阅读 sales.csv          preserved
S3 completed 委派异常点分析          preserved
S4 pending   补写结论，保留已有两节   modified
S5 pending   对照三个标题后再次提交   added

diff:
  preserved: [s1, s2, s3]
  modified:  [s4]
  added:     [s5]
  removed:   []
```

S4 在失败前被 Lead 标成 completed。进入 Replan 之前，Verify 节点把「对应该交付物、且 finding 为 failed」的步骤改成 blocked。演示里就是 S4。这样模型收到的输入已经是 blocked，而不是让它自己猜该改哪一步。

下一轮 Lead 看见的是第 8 章的第二次上下文：S1–S3 已完成，S4 要补写，S5 要求再提交。Lead 不需要重做阅读和委派，除非它自己还要 `read_file` 确认现有报告。那是工具选择，计划文本已经告诉它不要把已完成步骤当未做。

`guard.replan_count` 达到 3 时不再进入 Lead，Run 失败，原因 `max_replans_exceeded`。失败前仍走 Reflect，只写 lesson。

---

## 11. Verification

普通 assistant 文本不是完成。只有 LeadLoop 收到 `submit_for_verification` 才进入 Verify。

输入：

- `contract.deliverables`
- 磁盘上的对应文件
- 本 Run 的 ToolMessage 短结果（从 `messages` 里取）
- 最近 assistant 文本，仅当前三项都无法判断时使用

证据优先级：

```text
Artifact / 文件
    >
工具执行结果
    >
已有消息
    >
LLM 语义判断
```

对演示契约，前两级就够了，不应落到语义判断：

1. 对每个 deliverable，用工作区根路径拼接 `path`，禁止 `..` 逃出该 thread 目录。
2. 文件不存在：该条 failed，evidence 写「路径不存在」。
3. 文件存在但大小为 0：failed，evidence 写「文件为空」。
4. `must_contain` 的每个子串做普通字符串包含检查。缺少则 failed，evidence 写「文件存在且非空，正文没有该子串」。
5. 以上全过：该条 passed，evidence 写「已在文件中找到」。
6. 只有契约里出现无法用文件表达的条目时，才把工具结果和消息交给一次结构化模型调用。演示契约不设这种条目。模型不能把第 2–4 步已经 failed 的条目改成 passed。

输出覆盖 `findings`。任一 failed 则 `passed=false`，图走向 Replan。全部 passed 则走向 Reflect。

`report.md` 不存在时的时序：

```text
Contract.deliverables[0].path = artifacts/report.md
Lead 没有 write_file，却调用了 submit_for_verification
LeadLoop 退出
Verify stat 该路径，得到不存在
findings = {
  passed: false,
  items: [{
    criterion: "artifacts/report.md 存在且非空",
    status: failed,
    evidence: "artifacts/report.md 不存在",
    reason: "契约要求的交付物没有落到磁盘"
  }]
}
verification.completed
Replan
LeadLoop
```

演示主线用的是更紧的失败：文件在，但没有「结论」。时序与上面相同，只是 evidence 换成「正文没有结论」，并且 S4 被标成 blocked。通过时三个 `must_contain` 都是 passed，然后才允许 Reflect。

---

## 12. Subagent

```text
Lead 的模型发出 delegate_task
 ↓
LeadLoop 执行该工具
 ↓
SubagentExecutor
 ↓
独立 messages、独立模型调用、文件工具
 ↓
一段结果文本
 ↓
父 messages 里的一条 ToolMessage
 ↓
Lead 的下一次模型调用
```

父级传给子级的只有：`subagent_id`、task 字符串、workspace 根、contract 的 goal 与交付路径（让它知道不要改错文件）。不传父级的完整 messages、不传 Project Memory、不传 findings、不传父 plan 全文。task 字符串里由 Lead 写清要看的文件和要返回的格式。

子级不进入的东西，也是它不能做的事：不写项目记忆，不跑 Verify，不持有 plan，不能调用 `delegate_task`、`submit_for_verification`、`set_step_status`。工具只有 `list_dir`、`read_file`、`write_file`。演示里的异常点任务只需要读 csv，不要求它写报告。报告仍由 Lead 写，这样 Artifact 的责任在父级，Verify 也只检查父级交付物。

子图使用内存 checkpointer，key 是 `subagent_id`。工具返回后这份状态丢掉。父 thread 的 PostgreSQL Checkpoint 不包含子消息。父状态只增加：

- 一条 ToolMessage，内容是子级最终说明，截断到 4000 字符
- `delegations` 里一条 `{id, task, status, summary}`

同一时刻只跑一个 Subagent（`max_concurrent = 1`）。同一次模型响应里若有多个 `delegate_task`，按顺序执行，不建调度器。一个 Run 里总数超过 6，该工具直接返回失败 ToolMessage。子级自己的模型轮数上限 8。超时 3 分钟按 failed 收回。

子级失败不自动 Replan。Lead 看见失败的 ToolMessage 后自己做，或在提交后由 Verify 决定是否重规划。

---

## 13. Workspace / Artifact

```text
workspace/threads/{thread_id}/
├── uploads/     # 用户通过 Go 上传的原件。Agent 只读
├── work/        # 资料和草稿。Agent 可读写
└── artifacts/   # 交付物。Agent 可写。Verify 在这里查契约路径
```

Go 在创建 Thread 时建立这三目录。演示资料由种子或测试夹具写入 `work/`。Python 收到的 `workspace_root` 指向 `workspace/threads/{thread_id}`。

`read_file` / `write_file` / `list_dir` 把参数当成相对 `workspace_root` 的路径。绝对路径、`..`、以及写到 `uploads/` 都拒绝，并返回失败 ToolMessage。`read_file` 允许三个目录。`write_file` 只允许 `work/` 和 `artifacts/`。

一次真实写入：

```text
work/sales.csv 已在磁盘上
 ↓
Lead read_file("work/sales.csv")
    工具用 os 读字节，截断后放进 ToolMessage
 ↓
Lead read_file("work/notes.txt")
 ↓
Subagent 再读 work/sales.csv（它自己的消息，不进父 checkpoint）
 ↓
Lead write_file("artifacts/report.md", 正文)
    工具 os 写文件
    写完后 stat 成功
    state.artifacts 并入 "artifacts/report.md"
 ↓
Go 不在写文件的瞬间抄一份正文，也不建 artifact 元数据表。
Python 在 run.completed 的 payload 里带相对路径。
Go 把路径抄进该 Run 的 `artifact_paths`，供 CLI 列出。文件是否存在仍以磁盘为准。
 ↓
Verify 再次打开 workspace_root/artifacts/report.md
    不以 state.artifacts 里有这条字符串作为文件存在的证据
```

若进程在 `write_file` 返回成功前崩溃，Checkpoint 里没有这条 artifact，磁盘可能有半截文件。同一次 Run 的 `resume` 从最后一个已提交节点继续，见第 19 章：尚未进入 Checkpoint 的那次写入可能再执行一遍。目录扫描不能代替这次恢复。Verify 仍以当时磁盘内容为准。

---

## 14. Middleware

只有三个。它们是每次模型调用旁边的钩子，不决定契约、计划、通过与否、记什么长期记忆。

| Middleware | 钩子 | 输入 | 输出 | 为什么是 Middleware | 为什么不是业务 |
| --- | --- | --- | --- | --- | --- |
| ContextMiddleware | `wrap_model_call` | 当前 State，以及这次读到的 memory 行 | 替换后的 ModelRequest。不改 State | 每次模型调用都要重建输入，挂在调用边上才不会漏 | 它不决定 Plan 长什么样，只是把已经写好的 State 摊开 |
| SummarizationMiddleware | 普通模型调用前，若 `messages` 估算超过 6000 token 或条数超过 24 | 除最近 8 条以外的旧消息，加上已有 summary | 用新 summary 替换 `state.summary`。从 `messages` 删除被摘要的那些消息 | 这是历史体积的横切限制，Lead 不该在业务工具里记得压缩 | 摘要不是 lesson，也不写入项目记忆 |
| TokenBudgetMiddleware | 普通模型响应之后 | 响应里的 usage | `guard.total_tokens` 累加。达到 200000 则让图走向失败 | 防止展品跑飞账单和死循环 | 它不看任务有没有做完，只看消耗 |

普通 Agent 模型调用的顺序是 ContextMiddleware → SummarizationMiddleware → TokenBudgetMiddleware → Model。

Summarization 内部那一次调用不走这条链。它直接把「旧消息 → summary」交给模型，不再进入 ContextMiddleware 或 SummarizationMiddleware，因此不会递归摘要，也不会为这次内部调用再拼一份 Context。这次调用的 token 直接加到 `guard.total_tokens`，不重新进入中间件。

业务节点禁止塞进这条链。Verify 如果放进 middleware，就会在每次模型调用时都当一次闸门，和「只有 submit 才检查」冲突。Memory 如果放进 middleware 的 `after_agent`，写入时机就绑在库的代理结束语义上，而本图的结束点是 Reflect。

Skill 痕迹：进程启动时扫描 `python/skills/*/SKILL.md` 的标题。Context 固定规则下面加一段「可用技能名称」。目录为空则这段为空。没有加载正文的工具。

---

## 15. Event

```text
Python 节点或工具边界
 ↓
LangGraph custom stream
 ↓
EventSink 为该 run_id 分配 seq，POST /internal/runs/{run_id}/events
 ↓
Go 校验 run 存在，按 (run_id, seq) 插入
 ↓
PostgreSQL events
 ↓
GET /api/runs/{run_id}/events    text/event-stream
 ↓
CLI run watch
```

事件是某次状态变化的记录，不是另一份 AgentState。先改 State 或先落盘，再发事件。发送失败只打 warning，同一 body 重试。Agent 不因为页面没收到事件而重做工具。

```json
{
  "run_id": "uuid",
  "seq": 17,
  "type": "verification.completed",
  "timestamp": "2026-10-07T12:00:00Z",
  "payload": {}
}
```

`seq` 由 Python 在该 Run 内从 1 递增，不跨 Run 共用计数。对象一旦生成，重试不换号。Go 不重新编号。唯一约束 `(run_id, seq)` 让重复 POST 变成成功的空操作。序号允许空洞，CLI 按 seq 排序。

SSE 每条：

```text
id: 17
event: verification.completed
data: {"run_id":"...","seq":17,"type":"verification.completed","timestamp":"...","payload":{}}
```

断线恢复认 `Last-Event-ID`，也认 `?after_seq=17`。只发送 `seq` 更大的行。

| type | 谁发出 | payload |
| --- | --- | --- |
| `run.started` | 图启动 | `{ "goal": "..." }` |
| `model.message` | Lead 或子代理把一段给用户看的 assistant 文本写入消息之后。工具调用轮如果没有可展示正文，可以不发 | `{ "role": "assistant", "content": "..." }` |
| `tool.started` | 执行工具之前 | `{ "tool": "read_file" }` |
| `tool.completed` | 执行之后。失败也用这一条，`status` 为 `failed` | `{ "tool", "status", "summary" }`。summary 是短句，不是文件正文 |
| `subagent.started` | executor 进入之前 | `{ "subagent_id", "task" }` |
| `subagent.completed` | 收回之后。失败则 `status=failed` | `{ "subagent_id", "status", "summary" }` |
| `plan.updated` | EnsurePlan、`set_step_status`、Replan 写完 State 之后 | `{ "plan": { "goal", "steps": [{ "id", "title", "status" }] } }`。这是快照 |
| `verification.completed` | Verify 写完 findings | `{ "passed": false, "findings": [{ "criterion", "status", "evidence" }] }` |
| `replan.started` | 进入 Replan 时 | `{ "reason": "verification_failed" }` |
| `memory.written` | 每条插入成功 | `{ "kind": "lesson", "summary": "最多 120 字" }`。不是记忆全文 |
| `run.completed` | Reflect 之后 | `{ "summary", "artifacts": ["artifacts/report.md"] }` |
| `run.failed` | 护栏或非法 Replan | `{ "reason", "message" }` |
| `run.cancelled` | 取消边界 | `{ "reason": "user_cancelled" }` |

没有 contract 事件、没有 context 事件、没有 middleware 事件。

---

## 16. Go / Python 边界

| 数据 | 真相在哪 | 另一侧看到什么 |
| --- | --- | --- |
| Project、Thread、用户 Message、Run 状态 | Go 的表 | Python 只在启动参数里拿到 id 和 goal |
| Event | Go 的 `events` 表。内容由 Python 产生，Go 不改 payload | CLI 只经过 SSE 和已有事件的 GET |
| Workspace 目录与文件字节 | 磁盘。Python 工具读写 | Go 的 Run 只保存相对路径快照。Go 不解释报告内容，不建 artifact 元数据表 |
| AgentState（messages、contract、plan、summary、findings、artifacts 列表、delegations、guard） | Python Checkpoint（`PostgresSaver`） | Go 不读 Checkpoint。CLI 上的 Plan / Verification 来自事件投影和 Go 为刷新保存的最新快照 |
| Project Memory 全文 | Python 的 `PostgresStore` | CLI 的活动流只有 `memory.written` 的短 summary。Go 不读取记忆来做决策 |
| 模型选择与工具执行 | Python | Go 不知道这次有没有调用模型 |

三份东西不要混：

```text
Python Checkpoint = Agent 继续执行时的事实源
Go Event         = 运行过程的可观察记录
Go Run Snapshot  = CLI 刷新用的物化视图
```

`runs.plan_snapshot`、`verification_snapshot`、`artifact_paths` 和由终态事件改写的 `status` 都是这第三份。它不是第二份 AgentState。`run show` 读它，才能在没有 SSE 时看见当前计划、最近一次验证和产物路径。

会改这些字段的事件，插入 `events` 和更新对应快照必须在同一事务里。不用事务也可以，但必须有等价的失败恢复，不能留下「事件已经是新的，Run 快照永远是旧的」。这只约束 Go 的观察面。不因此做事件溯源、通用投影框架或消息队列。Agent 下一步仍只读 Checkpoint。

Go 调用 Python。Python 立刻返回 202，图在请求外执行：

```http
POST /internal/runs
{
  "project_id": "",
  "thread_id": "",
  "run_id": "",
  "goal": "",
  "workspace_root": ""
}

202 Accepted
{ "run_id": "", "status": "accepted" }
```

Python 调用 Go：

```http
POST /internal/runs/{run_id}/events
```

取消先改 Go 的 `runs.status`，再通知正在跑的 Python：

```http
POST /internal/runs/{run_id}/cancel
```

恢复用同一个 thread 上尚未结束的 Checkpoint：

```http
POST /internal/runs/{run_id}/resume
```

Python 在启动和恢复时读取该 Run 的 status。内部事件接口只监听 Go 能访问的地址，不交给浏览器。

---

## 17. CLI Projection

展示层只有终端。无参数的 `cakerdesk` 进入 Terminal Agent Shell。管理命令仍在，用来查看和调试。两者都不持有 AgentState，也不决定下一步。

```text
cakerdesk
cakerdesk serve
cakerdesk project ...
cakerdesk thread ...
cakerdesk run start|show|watch|resume|cancel
cakerdesk artifact list
cakerdesk memory list
```

Shell 是输入入口加事件投影。启动时用现有的 list 和 create：没有 Project 或 Thread 就问一个名字并创建；只有一个就直接用；多个就按编号选。之后记住当前的 `project_id` 和 `thread_id`。

普通一行文字走现有的创建 Run，再用现有 SSE 读事件，并用同一套投影打出短句。Run 到达 `run.completed`、`run.failed` 或 `run.cancelled` 后回到 `>`。下一次输入仍用同一个 Project 和 Thread。

以 `/` 开头的只有 `/help`、`/new`、`/switch`、`/memory`、`/artifact`、`/cancel`、`/resume`、`/exit`。它们调用已有的 Go API。Shell 不访问 Python，不读 Checkpoint，不保存计划，也不判断任务有没有完成。

`run show` 是当前快照，`run watch` 是时间线。两个命令不合成一个接口。

`run show` 打印：

- Run 状态：running / cancel_requested / completed / failed / cancelled
- Plan 步骤和 status
- 最近一次 Verification 的 passed 和 findings
- Artifacts：Run 上的路径快照。`artifact list` 再对磁盘做存在性核对

`run watch` 先按 seq 打出已有事件，再接 SSE，接成同一条时间线。它消费的是事件，打印的是给演示者看的短句，不是 SSE 里的 JSON。

```text
Event
 ↓
Go events 表
 ↓
CLI Projection
 ↓
人可读的一行
```

每种事件只投影自己 payload 里已经有的字段：

- `tool.completed`：工具名和短摘要
- `verification.completed`：passed、失败的 criterion、短 evidence
- `plan.updated`：当前步骤和 status。和本条时间线里上一次计划相比，能看出保留、重新打开和新增，这个比较只存在于本次输出
- `replan.started`：`verification_failed`
- `subagent.started`：任务摘要
- `subagent.completed`：结果摘要
- `memory.written`：fact 或 lesson 的短摘要
- `run.completed` / `run.failed` / `run.cancelled`：终态和原因

CLI 不重算 Plan，不重判 Verify，不决定要不要 Replan，不读 Checkpoint，不另存一份 AgentState。它不请求 Python 做决策，不调用模型，不写记忆。`memory list` 只读已经写好的项目记忆。

演示主线在输出里的变化：

1. `run start` 之后，状态是 running，时间线出现 run.started。
2. `plan.updated` 打出 S1–S4，全是 pending。
3. 每次 `tool.completed` 追加一行短结果。S1、S2 随后变成 completed。
4. `subagent.started` 打出「分析异常点」进行中。`subagent.completed` 后变成完成，并带一句摘要。S3 completed。
5. 模型若说出「报告写好了」，这句能看到，状态仍是 running。这时还没有 Verification。
6. `verification.completed` 且 passed 为 false。「结论」失败，evidence 为文件里没有该标题。
7. `replan.started` 打出 verification_failed。紧接着的 `plan.updated` 把 S4 显示为 pending（补写），并出现 S5。S1–S3 仍是 completed。
8. 再次出现写文件的工具事件，然后第二次 `verification.completed` 为通过。
9. 时间线出现 memory.written，只显示 lesson 的短摘要。
10. 状态变为 completed，产物路径列出 `artifacts/report.md`。

在第 7 步之后重新 `run watch`：先拿到 Go 上的 plan 快照和 failed verification，再从最后 seq 继续。不重新推断「既然失败了就该重规划」。

不展示：每一层 context 的 token、middleware 顺序、AgentState JSON、Checkpoint 行。

---

## 18. State / Data Flow

把演示里「第一次提交失败」到「第二次模型看见新计划」收成一条数据流，用来核对没有两份真相：

```text
write_file
  磁盘 artifacts/report.md 出现（缺结论）
  Checkpoint.artifacts 增加该路径
  tool.completed 发给 Go

submit_for_verification
  LeadLoop 离开
  Checkpoint.messages 多了提交记录
  不写 passed

Verify
  读磁盘，不读「Agent 说写好了」
  Checkpoint.findings.passed = false
  把 S4 改成 blocked
  verification.completed 发给 Go
  同一事务里写入 events，并更新 verification_snapshot

Replan
  读 findings 和 plan
  Checkpoint.plan = v2，含 diff
  replan.started 与 plan.updated 发给 Go
  plan.updated 与 plan_snapshot 一起落盘

Lead 下一次模型调用
  ContextManager 读 Checkpoint 的 plan v2 和 findings
  另读该 project 的记忆项（此时还没有本 Run 的新项）
  只在请求里拼出第 8 章的第二段
  Checkpoint.messages 不增加这段 System
```

通过之后：

```text
Verify 写 findings.passed = true
Reflect 只往 PostgresStore 写入记忆项
Checkpoint 不保存记忆副本
memory.written 只带短 summary
下一次 Run 的 ContextManager 用 project_id 再读这些行
```

---

## 19. Failure / Guard

| 条件 | 结果 |
| --- | --- |
| 连续 3 次模型响应没有 tool call | `run.failed`，`plain_text_loop` |
| 同一工具加同一参数连续 3 次 | `run.failed`，`repeated_tool_call` |
| `guard.replan_count` 达到 3 | 不再 Lead，`run.failed`，`max_replans_exceeded`。先 Reflect lesson |
| 累计 token 达到 200000 | `run.failed`，`token_budget_exceeded` |
| Replan 两次改掉 completed 步骤的 id 或 title | `run.failed`，`invalid_replan` |
| 路径逃出 thread 工作区 | 该工具失败，Lead 继续。不单独发明一种安全子系统 |
| 事件 POST 失败 | 重试同一 seq。不影响工具结果和 State |
| Contract 或 Plan 初始化失败 | `run.failed`，不 Reflect |
| Lead 已执行后的运行时失败 | 先 Reflect lesson，再 `run.failed` |
| 用户取消 | 下一个循环边界 `run.cancelled`，不 Reflect |
| 子代理超时或轮数用尽 | 父级收到 failed ToolMessage，Run 不因此结束 |
| EnsureContract 无法得到带路径的交付物 | `run.failed`，`invalid_contract`。不进入 Lead |

这些是停机和拒绝，不是策略引擎。

进程退出和工具只执行一次不是同一件事。恢复用的是 `PostgresSaver`、`durability="sync"` 和产品 `thread_id`，所以续跑点是最后一个已经写入的 Checkpoint，不是最后一个已经发生的工具副作用。`write_file` 已经把文件写上、下一步 Checkpoint 还没落盘时，进程退出后再 `resume`，这次写入可能再执行一遍。文件工具要能再跑；Verify 仍只看磁盘。不为这件事增加副作用日志、幂等键、通用执行器、副作用注册表、调度器或 supervisor。

---

## 20. Non-Goals

不做：

- Provider abstraction、多模型路由、自带密钥管理产品
- MCP
- Extension / Plugin 系统
- Gateway、多渠道、IM
- 多角色 Agent（Research、Coding、Browser 等）
- Skill 正文、Skill 市场、Skill 运行时
- Vector memory、RAG、复杂记忆检索
- 容器级 Sandbox、多租户隔离
- 依赖图、多层规划、Planner 代理、任务调度器
- 多种 Verifier、评分器和独立验证产品
- 高可用、多副本抢主、RBAC、多租户
- 自动 supervisor、lease、heartbeat、Worker 队列
- 把每个工具包成 exactly-once 副作用框架，以及为此做的 task ledger、idempotency key、operation journal、side-effect table、recovery supervisor
- Event Sourcing、CQRS、通用投影框架、消息队列、Kafka、Redis

DeerFlow 里还有标题生成、澄清问题、看图、后台任务、引用收据、自定义 Agent 配置。它们对完整产品有用，但不出现在第 3 章的主线上，因此不做。

---

## 21. Design Rationale

State 和 Context 分开，是因为 Checkpoint 会原样恢复。DeerFlow 的 `_inject` 已经把「这一次给模型看的块」放在 `request.override`，而不是 `before_model` 返回新的 messages。若把 Contract、Plan、Memory 每轮 append 进 `messages`，下一轮会把上一轮的投影再包进去，压缩和预算都失去对象。

Memory 不进 Checkpoint，是因为 Checkpoint 的键是 Thread。项目里另一个 Thread 的 Run 读不到那份 state。跨 Run 的经验要按 `project_id` 放在自己的表里。放进 Checkpoint 还会让压缩和线程删除把经验一起带走。

普通文本不能结束 Run，是因为文本不产生可检查的证据。`create_agent` 的默认停机正好停在这里，所以展品不能把那个停机当成完成。`submit_for_verification` 把「我要交卷」变成一个显式边，Verify 才能在固定位置读磁盘。

Verification 不放 Middleware，是因为 middleware 包的是每一次模型调用。完成检查只该发生在交卷之后。放进 middleware 要么每轮都查文件，要么又变回「模型没调工具就检查」，闸门会消失。

Replan 是节点，是因为它要整份替换 Plan 并接受代码对 completed 步骤的检查，然后把控制权交回 Lead。这是图上的一条边，不是某次模型调用前后的装饰。Lead 的 `set_step_status` 只能改状态，避免模型在循环里悄悄改掉历史步骤。

Subagent 结果回到 ToolMessage，是为了让父循环的下一次模型调用用同一条「工具观察」规则消化它。子消息若并进父 Checkpoint，父级的摘要、预算和页面消息都会被局部分析淹没，隔离也就不存在。

Event 是观察层，是因为 CLI 和 Go 需要过程，但下一步该不该 Replan 已经由 Python 的边决定。CLI 根据 `plan.updated` 改自己的计划，就会在断线重连时和 Checkpoint 各讲各的。

Go 不参与决策，是因为契约、计划、验证和记忆的一致性都在同一条图里。若 Go 也保存一份可写的 Plan 并允许编辑，Python 下一轮就不知道该信谁。Go 只物化事件快照，供刷新使用。

CLI 只做投影，是因为展示主线要让人看见运行时的决定，而不是在命令里再实现一个运行时。

Checkpoint 能恢复，是为了证明 State 还在、Context 能重建、Plan 能变、Verify 能拒绝、Replan 能继续、Memory 能跨 Run、进程中断后还能从同一 Thread 接着做。它不证明任意工具的副作用只会发生一次。把这两件事写成一个框架，展品就会去补调度和幂等，而第 3 章的主线并不需要它们。

Go 的 Run 快照若可以和事件永久分叉，CLI 刷新看到的计划就不是刚才播过的那条过程。所以观察层要一起落盘。这仍然不是第二份可执行的 AgentState，下一步该不该 Replan 还是由 Python 的边决定。

---

## 22. Implementation Mapping

下面是终态地图，用来对照模块该落在哪。不为「以后好扩展」先放空接口。

```text
cakerdesk-next/
├── README.md
├── Makefile
├── .env.example
├── docs/agent-runtime-design.md
├── python/
│   ├── cakerdesk/
│   │   ├── main.py                 # POST /internal/runs 立即 202；cancel；resume
│   │   ├── schemas.py              # Contract、Plan、Reflection
│   │   ├── prompts/                # 各次调用的固定说明
│   │   ├── runtime/
│   │   │   ├── graph.py            # 组装第 5 章的图。PostgresSaver，durability=sync
│   │   │   ├── state.py            # AgentState
│   │   │   ├── lead.py             # LeadLoop 的 model 节点与路由
│   │   │   ├── context.py          # ContextManager
│   │   │   ├── middleware.py       # 三个 middleware
│   │   │   ├── memory.py           # PostgresStore 的读写
│   │   │   ├── plan.py             # 规范化与 PlanDiff 校验。不截取 JSON
│   │   │   ├── verify.py           # Verifier
│   │   │   └── subagent.py         # SubagentExecutor
│   │   ├── tools/
│   │   │   ├── definitions.py      # @tool：文件工具、set_step_status、submit、delegate_task
│   │   │   └── executor.py         # 执行这些调用。不是 ToolNode
│   │   └── infra/
│   │       ├── model.py            # 只构造 ChatOpenAI，并暴露 bind_tools / with_structured_output
│   │       ├── events.py           # 按 Run 分配 seq，并 POST Go
│   │       └── workspace.py        # 路径约束与扫描
│   └── skills/                     # 可空。只留 SKILL.md 痕迹
└── go/
    ├── cmd/cakerdesk/              # 无参数进 Shell；serve 启动 HTTP
    ├── internal/cli/               # Shell 与管理命令
    ├── internal/server/            # Gin handler，直接调用 sqlc
    ├── internal/db/                # sqlc 生成代码
    └── migrations/                 # projects threads messages runs events
```

```text
ContextManager
  build(state, memory_rows) -> messages_for_llm
  apply_budget(sections) -> sections
  不写 State

MemoryStore
  write(project_id, run_id, items) -> rows        # PostgresStore.put
  read_for_context(project_id) -> rows            # 最近 20 条

Planner
  ensure_plan(contract) -> plan
  replan(plan, findings) -> plan                  # 含 diff，随后由 validate_diff 检查

Verifier
  verify(contract, workspace_root, messages) -> findings

Replan 的 validate_diff(old, new) -> ok | 拒绝

SubagentExecutor
  run(task, workspace_root, contract_brief) -> {status, summary}
  父 Checkpoint 不接收子 messages

EventSink
  emit(run_id, type, payload) -> None             # 分配 seq 并 POST

LeadRouter
  route(ai_message) -> tools | verify | continue
```

Go 侧对应：`CreateRun`、`ResumeRun`、`IngestEvent`（幂等插入）、`StreamEvents(after_seq)`、`CancelRun`。取消先写 `runs.status=cancel_requested`。CLI 对应这些读接口，不在本地重放决策。

---

## 23. Behavioral Acceptance Criteria

下面每一条都要用真实模型或真实文件副作用观察，不能用空返回冒充。

- Lead 至少完成一轮：模型发出 `read_file`，观察进 `messages`，下一次模型请求能看见该观察，并且直到 `submit_for_verification` 才离开循环。
- 只返回普通 assistant 文本时，图仍停在 LeadLoop，不产生 `verification.completed`。
- 第一次 Lead 请求的上下文含 Plan v1。FAIL 和 Replan 之后的下一次 Lead 请求含 Plan v2 和 failed findings。两次请求的拼装 System 都不在 Checkpoint 的 `messages` 里。
- 把消息灌过摘要阈值后，Checkpoint 的 `summary` 改变，被覆盖的旧消息从 `messages` 消失，最近消息还在。
- Run A PASS 后，该 project 的记忆项里有 lesson。同一 project 的 Run B 第一次模型请求包含该内容。
- 契约要求 `artifacts/report.md` 且必须包含「结论」。磁盘上的文件没有这一标题时，Verify 为 FAIL，并进入 Replan。
- Replan 后的 plan 保留 S1–S3 的 id 与 title，S4 被修改，并出现新步骤。随后的 Lead 请求读到的是这份 plan。
- `delegate_task` 期间子级自己的模型消息不出现在父 Checkpoint。父 `messages` 里能找到那条结果 ToolMessage。
- `write_file` 之后磁盘上有非空文件。Verify 的 evidence 来自这次读取，而不是只看 `state.artifacts`。
- `run.started`、`tool.started`、`tool.completed`、`verification.completed`、`run.completed` 或 `run.failed` 出现在 Go 的 `events` 表，且 `(run_id, seq)` 唯一。SSE 用 `after_seq` 能只收到之后的事件。
- `run watch` 打出的是人能顺着读的短句：计划、工具、Subagent、提交、FAIL、Replan、新计划、再执行、PASS、Memory、completed。断线重连后，历史事件和实时事件仍是一条时间线。只收到 JSON 不算这条通过。
- 真实 Python 进程退出后，用同一个 `thread_id` 再 `resume`。Plan、Findings、Messages 还在，已经写好的文件还在，本 Run 的 bootstrap 不重跑，下一节点继续。Checkpoint 边界之前的文件工具允许再执行一次。只在同一进程里用 `interrupt_after` 停住，不能代替这次验收。
- `plan.updated`、`verification.completed`、`run.completed` 写入后，对应的 `plan_snapshot`、`verification_snapshot`、`artifact_paths`、`status` 不能永久停在旧值。插入和快照更新同事务，或有等价恢复。
- 产品路径使用 `PostgresSaver` 和 `PostgresStore`。`MemorySaver` 与 `InMemoryStore` 只作为测试替身，不是线上的持久化实现。
- 重启后仍能从 `runs.status` 看出取消已经提出。

---

## 24. Implementation Phases

施工顺序以本章为准。第 23 章是终态行为。每一阶段先通过自己的验收，再进入下一阶段。模型调用边界和 Go 产品库都不是项目终点。

| 施工阶段 | 只做这些 | 纵向验收 | 此阶段明确不做 |
| --- | --- | --- | --- |
| 1. 模型调用边界 | Prompt 文件、Pydantic、`@tool`。真实模型用 `ChatOpenAI` 的 `bind_tools` 与 `with_structured_output`。测试替身返回同形状的结果，不重做 LangChain 适配 | 周报行为测试仍通过。不再从文本截 JSON，工具 schema 不再是空对象 | 更换 Checkpoint、更换 Go、删除 CLI 之外的展示 |
| 2. 真实主链 | Contract、Plan、Replan、Reflect、Lead、Subagent 分别走结构化输出或绑工具的调用。Verify 仍只查磁盘 | 缺「结论」仍 FAIL，补写后 PASS。普通文本不进入 Verify | `create_agent`、`ToolNode`、Provider 工厂 |
| 3. Checkpoint | `PostgresSaver`，`durability="sync"`，`thread_id` 为产品 thread。表由 `setup()` 创建 | 中断后同一 thread 的状态仍有 Plan、Findings、Messages | 自建 checkpoint 表，或把它们写进 Go migration |
| 4. Resume | 图已经结束或尚未开始时才初始化本 Run 字段。`resume` 从已有 Checkpoint 的下一节点继续 | 崩溃后恢复不重置计划、发现和已写文件 | 自写节点调度器，把每个工具包成 `task` |
| 5. Memory 与事件 | `PostgresStore`。节点把事件写入 custom stream，EventSink 按 Run 从 1 编号后 POST Go | Run B 的模型输入看见 Run A 的 lesson。两个 Run 的 seq 各自从 1 开始 | 向量、自建记忆表、Go 重编号 |
| 6. Go 产品面 | Gin、sqlc、goose、`pgxpool`。创建 Run 只等 202。没有 `artifact_meta` | 产品状态在 PostgreSQL。没有库的测试跳过，不退回 SQLite | Service 层、Redis、队列 |
| 7. CLI | `project`、`thread`、`run start\|show\|watch\|resume\|cancel`、`artifact list`、`memory list`。`watch` 先历史再实时 | 取消写入 `runs.status`。进程重启后仍能看出该 Run 已被请求取消 | supervisor、lease、heartbeat |
| 8. 收口 | 确认没有 `web/` 和静态文件服务。`watch` 把原始 JSON 收成可读时间线。一次真实进程退出后的 `resume`。Event 与 Go 投影不永久分叉。Python 按 runtime、tools、infra 归位。README、`.env.example`、Makefile 固定启动路径。无参数进入 Terminal Agent Shell，管理命令保留 | 第 3 章主线能从 Shell 看完，且上面几项都成立 | Web、平台化、supervisor、scheduler、worker queue、lease、heartbeat、自动重试框架、分布式恢复、exactly-once、Kafka、Redis、多 Agent、多 Provider |

---

## 25. 实施约束

这是展品，不是产品。完成标准的优先级：

```text
S  核心行为真实
A  核心模块互相连通
B  一条完整 Demo 主线跑通
C  CLI 能把过程展示清楚
D  工程边界和异常处理
E  生产级完整性
```

做到 S/A/B/C，再加上第 23 章里那几项必要的 D，项目就完成。不要求 E 里的生产级能力。

四个核心，按这个深度做：

- Context 和 Memory 做深。时间不够时先保住这两条的真实行为。
- Verification → Replan 只做闭环，不追求复杂。
- LeadLoop 保证系统能连续工作。

Subagent、Go、Event、CLI 是主链稳定之后的展示层。无参数进入的 Shell 是演示入口，管理命令用来查看和调试。Shell 让人看见 Agent 在做什么，不持有 AgentState。

一个抽象如果只有一个实现，而且不解决当前主线，就不创建。不提前做 `ProviderFactory`、`ToolRegistry`、`AgentRegistry`、`SkillRegistry`、`MemoryBackend`、`SandboxProvider`、`EventBus`、`PluginManager`、`ExtensionManager`。

Demo 可以固定输入文件、固定任务和固定模型配置，也可以用测试 EventSink。核心行为不能写成固定答案：`verify` 永远通过、`memory.read` 固定返回某条 lesson、`replan` 固定返回预设 Plan、`subagent` 直接返回预设结果、`context` 返回固定 prompt、artifact 只改 State 不写文件、事件只打日志。

工程问题按这个分类：

- P0，立刻修：Context 污染 Checkpoint，Memory 进不了下一 Run，Verify 永远 PASS，Replan 不改变行为，Subagent 没有独立执行，Artifact 没有落盘。
- P1，尽快修：SSE 丢掉关键事件，CLI 看不见 Replan，CLI 只能看见原始 JSON，产物路径失效，Run 状态错乱，事件已写入但 Go 快照永久没有同步，`resume` 重新 bootstrap 并覆盖已有 Plan、Findings、Messages。
- P2，记下但不做：exactly-once、通用副作用日志、自动故障恢复、Supervisor、Scheduler、Worker Queue、Lease、Heartbeat、高可用、多副本抢主、分布式一致性平台、高并发治理、复杂重试、RBAC、多租户、Provider Factory、Plugin 或 Skill Registry、Event Bus、Redis、Kafka。

某一阶段已经同时满足「真实运行、真实影响下一步、演示里看得见」，该阶段就结束，然后进入下一阶段。第 3 章的主线能够完整运行，下一次 Run 能看见前一次的 Memory，真实进程中断后能从同一 Thread 的 Checkpoint 继续，Shell 能从历史事件跟到实时事件，Go 的事件和 Run 投影不永久分叉，这个展品就完成了。README、Makefile 和目录归位只是把这条路径固定下来。其后增加的能力，包括 Web 和平台化，不再属于本项目的完成标准。

实现时只问：为了证明 Long-Horizon Agent Runtime，现在还缺哪一跳。不问 DeerFlow 还有什么没搬过来。
