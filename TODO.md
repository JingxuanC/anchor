> **Historical snapshot (2026-11-15).** This board was written against
> `feature/core-events` @ `35a2a46`, before the handoff gate, code transport,
> adapters, `anchor wrap`, the MCP server and the A/B benchmark landed. The
> authoritative, maintained status is
> [`docs/09-implementation-status.md`](docs/09-implementation-status.md); keep
> this file only as a planning/history artifact.

# Anchor TODO / Gap Board

- 生成日期：2026-11-15
- 代码基线：`feature/core-events` @ `35a2a46`（本 TODO 提交前）
- 依据：`/workspace/Anchor-PRD-v0.3.md`（并对照 v0.1/v0.2）、`docs/00`–`08`、`acceptance/matrix.yaml`、`/workspace/anchor-*-recheck-*.md`
- 最近独立模块复验：`/workspace/anchor-init-export-recheck-35a2a46.md`：86 PASS / 3 helper SKIP / 0 FAIL；只覆盖该报告列出的模块，不是完整产品验收。
- 本文件是当前状态板。`acceptance/matrix.yaml` 仍是**计划基线**（`status: planned`），不是实际完成状态；README、`docs/06` §9 的状态文字也早于当前代码，已列入 `TODO-DOC-01`。
- 功能范围不缩减。优先级只表示交付顺序，不表示删除低优先级功能。

## 状态图例

| 标记 | 含义 |
|---|---|
| `[V]` | 已实现，且被独立复验报告在该范围内验证过（不等于完整产品通过） |
| `[F]` | 只有 fixture / recorded / mock / contract 验证，未接真实 Agent 或真实环境 |
| `[P]` | 局部实现；部分行为可用，但需求未闭合 |
| `[N]` | 未实现 |
| `[E]` | 环境未验证（平台、真实 Agent、真实服务或运行时未测） |

## 1. 当前已验证基线

| 能力 | 状态 | 证据/边界 |
|---|---|---|
| 事件追加、确定性重放、因果 DAG、冲突隔离、revision、tombstone、非法操作拒绝 | `[V]` | `anchor-core-recheck-9eea889.md`、`anchor-core-recheck-ade6116.md`；仅核心段 |
| 同节点并发 transition 一胜一负；独立节点并发；raw append 因果/revision 门禁 | `[V]` | 真实 CLI barrier 回归；Unix |
| 单批次 manifest 原子发布、崩溃 temp 隔离、旧裸数组/单对象拒绝 | `[V]` | `anchor-format-recheck-5ae5ee0.md`、`anchor-core-recheck-ade6116.md` |
| evidence 内容寻址、重算 hash/大小、节点归属、done 门禁、只读 orphan audit | `[V]` | `anchor-core-recheck-ade6116.md`；Unix 证据打开 |
| init 空目录策略、manifest 丢失 fail-closed、共享 Agent 路径零副作用、并发 init 一胜 | `[V]` | `anchor-init-export-recheck-35a2a46.md` |
| fixture source 身份、Session registry、跨进程幂等、closed-session 策略 | `[F]` | `anchor-session-recheck-cfb6f0b.md`；`source=fixture/hook_unverified`，不是 native hook |
| adapter contract D/unverified、dry-run render、显式目标、不覆盖共享配置 | `[F]` | `anchor-adapter-recheck-d9ec0d8.md`；配置 fragment 仍是 documentation-only |
| 本地 export / import 校验 dry-run、路径/哈希/秘密/上限门禁、不执行包内容 | `[P]` | `anchor-init-export-recheck-35a2a46.md`；不恢复 workspace/events，不 apply code |
| 三适配器真实 native hook / 跨 Agent 恢复 | `[N]` | 三适配器均 `D/unverified/e2e=false`；无真实 Agent e2e |

## 2. 最高优先级剩余项（P0 关键路径）

1. **补完 Session Tree 数据模型**：constraints / success criteria、decision 结构化字段、branch fork/merge、provenance/confidence、user attestation、drift policy、summaries。
2. **补完真实恢复链**：code commit/dirty bytes 的 apply 事务、successor 验证事件、context injection、跨 Agent/跨设备恢复；本地 export/import 不等于恢复。
3. **真实 Claude Code adapter**：install/uninstall、hooks、rules、MCP 注册、context；Codex/Gemini 先 contract，再安排真实 e2e。
4. **causal-memory remote MCP bridge**：tools/list 发现、搜索注入、proposed→confirmed、证据/用户确认、重试/离线/限额；不得本地实现引擎或必经代理。
5. **Git sync**：事件 union、config/manifests、tombstone、冲突、ownership fencing、无 force-push、无 `causal.db`。
6. **统一 config/rules/skills + vault**：managed blocks、drift/rollback、keychain、secret refs、运行时注入、审计；不得把秘密写入上下文/日志/同步。
7. **CLI 完整面和 Web UI**：所有命令、JSON/exit contract、tree/evidence/sync/ownership/causal 视图、loopback/CSRF/Origin。
8. **发布工程**：migration/upgrade/rollback、uninstall、packaging/signing/SBOM、CI/e2e/contract/security/coverage、Windows/macOS 验收。
9. **安全闭合**：MCP/Git/web/hook 的路径、命令、超时、秘密、注入；当前路径防护仍非 openat2 级。
10. **状态文档同步**：README、`docs/06` §9、`acceptance/matrix.yaml` 必须从 planned/pre-implementation 更新为实际状态；保留 planned 与 actual 的区别。

## 3. Workspace / 数据格式

| ID | 需求 | 状态 | 剩余工作 | 验收标准 | 依赖 |
|---|---|---|---|---|---|
| TODO-WS-01 | REQ-WS-001, REQ-WS-004（Unix） | `[V]` | 保持；补独立回归到发布门禁 | init 只对不存在/空目录；已有日志/残留 fail-closed；并发 init 一胜；锁/批次/崩溃恢复通过 | 无 |
| TODO-WS-02 | REQ-WS-004 Windows | `[P]`（平台 `[E]`） | Windows LockFileEx/崩溃释放、真实 fault injection | Windows 上活锁不抢占、崩溃可恢复、双 init/写者不破坏日志 | Windows runner |
| TODO-WS-03 | REQ-WS-002, REQ-WS-006 | `[P]` | 落地可读/可 diff 的 `TREE.md`/`state.json` 类投影，确定性重建，禁止隐藏状态 | 删除投影可完整重建；重复重建字节一致；无手改权威状态 | 核心事件日志 |
| TODO-WS-04 | REQ-WS-003 | `[N]` | 全局/事件/证据/session schema 的迁移、备份、dry-run、rollback | 明确版本检查；迁移 dry-run 只读；可回滚；旧数据不静默破坏 | WS-03；迁移方案评审 |
| TODO-WS-05 | REQ-WS-007 | `[P]` | export/import 目前只提取包内字节，不重建 workspace/events，不 apply code；补独立 inspect/导入契约 | 新空目标可重建 workspace 或明确 incomplete；无 Anchor 也能读清单；不覆盖现有数据 | RST-02；code apply 设计 |
| TODO-WS-06 | REQ-WS-008 | `[N]` | uninstall 保留数据、`--purge` 显式确认 | 卸载只删集成文件；数据保留；purge 需确认 | 安装器 |
| TODO-WS-07 | REQ-WS-005 | `[P]` | 当前本地包不泄露绝对 repo 路径，但同步尚未实现；全量同步面需检查 | 同步数据无设备绝对路径；设备本地数据隔离 | SY |
| TODO-WS-08 | REQ-WS-003 / OPS-002 | `[N]` | upgrade/downgrade、备份、回滚、manifest 身份恢复 | 升级/回滚可 dry-run；旧日志身份必须显式核实；不自动重绑 | WS-04；迁移工具 |

## 4. Session Tree

| ID | 需求 | 状态 | 剩余工作 | 验收标准 | 依赖 |
|---|---|---|---|---|---|
| TODO-ST-01 | REQ-ST-001 | `[P]` | 增加 goal 的 north star、must/must-not constraints、success criteria 的结构化模型和 CLI | 每个 goal 有约束/成功标准；漂移检查可读取 | 数据模型/迁移 |
| TODO-ST-02 | REQ-ST-002 | `[P]` | decision 节点增加 decision/rationale/alternatives/consequences/provenance 字段 | decision 可结构化创建、查询、展示；旧 title/note 迁移明确 | 数据模型 |
| TODO-ST-03 | REQ-ST-003 | `[V]` | 保持 task 状态机；补 goal/branch 边界回归 | 合法/非法状态表与文档一致；非法转换无写入 | 无 |
| TODO-ST-04 | REQ-ST-004 | `[P]` | branch fork/merge/abandon、hypothesis、lineage 和冲突策略 | fork 保留来源；merge/abandon 可审；循环/父级不破坏 | ST-02 |
| TODO-ST-05 | REQ-ST-005, REQ-ST-006 | `[P]` | provenance enum（user_confirmed/agent_inferred/imported）、confidence、actor/agent 版本/session/timestamp 全覆盖 | 任何节点/事件可追溯来源；agent 不能伪造用户来源 | ST-13 |
| TODO-ST-06 | REQ-ST-007, REQ-ST-008, REQ-ST-009 | `[V]` | 保留 append-only、幂等、revision、tombstone、父子/环/转换约束；补 branch lineage 到 ST-04 | 事件不可变；tombstone 不回生；并发 edit/delete 显现冲突；非法树拒绝 | 无 |
| TODO-ST-07 | REQ-ST-010, REQ-ST-011 | `[P]` | drift warning/policy、merge/reparent/annotate、用户修正命令 | 越界行为警告；策略阻断只在可执行边界声明；用户修正可审计 | ST-01/02/05；adapter enforcement |
| TODO-ST-08 | REQ-ST-012 | `[N]` | summary 节点/字段、source pointer/hash、非权威语义 | summary 有来源和哈希；不得当 canonical history；不得当 evidence | ST-02；CAP |
| TODO-ST-09 | REQ-ST-013 | `[N]` | 本地用户 attestation（TTY/UI/signed action）；保留 override fail-closed 直到有可信机制 | agent 自报 user_confirmed 永远拒绝；本地确认可审计 | UI/TTY；安全评审 |
| TODO-ST-10 | Session registry / Session Tree | `[P]` | 当前 `Tree.Sessions` 是 adapter session→node 映射，不是完整 Session Tree 节点层级；补正式层级/恢复展示 | session 在树中有明确身份/生命周期；不把 registry 当完整恢复 | ST-01/04；adapter |

## 5. Capture / Collector

| ID | 需求 | 状态 | 剩余工作 | 验收标准 | 依赖 |
|---|---|---|---|---|---|
| TODO-CAP-01 | REQ-CAP-001/005 | `[F]` | 三适配器只支持 fixture/hook_unverified ingest；接真实 Claude Code hook，Codex/Gemini contract 后真实 e2e | 真实 hook 归一化事件；标签 real/recorded/fake 明确；无权不安装 | 真实 CLI 环境；AD |
| TODO-CAP-02 | REQ-CAP-002/009 | `[N]` | native hook fail-open、doctor 捕获失败可见、错误/日志语义 | 捕获失败不阻塞 agent；doctor 显示 adapter/source/version/错误 | AD；CAP-01 |
| TODO-CAP-03 | REQ-CAP-003/004/008 | `[P]` | 当前只采元数据、session/cwd 哈希、secret scan、1MiB 上限；补可配置 transcript/tool I/O 指针、redaction、大小限制 | 敏感内容不落盘；超限/拒绝路径可见；只存指针/hash | SEC；数据模型 |
| TODO-CAP-04 | REQ-CAP-006/007 | `[F]` | fixture 级幂等、双进程、离线已验证；真实 hook 仍需验证 | 同 source ID 一次、不同 ID 保留；离线核心可用 | CAP-01 |
| TODO-CAP-05 | REQ-CAP-009 | `[N]` | capture 错误进入 doctor/log；不得静默丢失 | 可查询最近失败/降级状态 | CAP-02 |

## 6. Restore / handoff

| ID | 需求 | 状态 | 剩余工作 | 验收标准 | 依赖 |
|---|---|---|---|---|---|
| TODO-RST-01 | REQ-RST-001/010 | `[P]` | packet 有目标/节点/session/evidence/code hashes；缺 constraints/next steps/ownership/provenance 标签的完整语义 | packet 字段齐全；user/inference/unverified 标签可读 | ST-01/05；WS-05 |
| TODO-RST-02 | REQ-RST-002/003/005 | `[N]` | context injection、successor ack/verify、跨 Agent 双向恢复 | 真实 adapter 注入；successor 验证事件；Claude/Codex/Gemini 矩阵或明确 skip | AD；ST-05；evidence |
| TODO-RST-03 | REQ-RST-004 | `[V]` | 保持摘要/元数据不能 complete；本地包仍 `incomplete` | 只有清单不得 complete；missing bytes 永远 incomplete | 无 |
| TODO-RST-04 | REQ-RST-007/008/009/011 | `[P]` | 有 branch/HEAD、diff/untracked hashes 和可选字节；无 Git commit object/bundle、不 apply code；补 dry-run/backup/force 确认 | dirty restore 需显式确认；应用可回滚；缺失/过期如实报告；无原机器绝对路径 | O-007；Git 安全设计 |
| TODO-RST-05 | REQ-RST-006 | `[N]` | 跨设备恢复依赖 sync；当前只有本地 export/import | 新设备上完整/部分恢复状态可验证；不把本地包当跨设备完成 | SY；RST-04 |
| TODO-RST-06 | REQ-RST-001..011 | `[P]` | export/import 写 target 不是事务性；失败 export 会留 `handoff.Prepare` 回执；需明确回滚/清理或文档 | 失败不产生误导性完整包；临时的回执/包状态明确 | RST-04；WS-05 |

## 7. Adapters / config / skills

| ID | 需求 | 状态 | 剩余工作 | 验收标准 | 依赖 |
|---|---|---|---|---|---|
| TODO-AD-01 | REQ-AD-001/002/003/004 | `[N]` | 三适配器 CLI 检测/版本、install/uninstall、hooks、MCP 注册、rules/instructions；Claude 先真实 | 真实 Claude e2e；Codex/Gemini contract+真实或明确 skip；保留用户配置 | 真实 CLI；O-003 |
| TODO-AD-02 | REQ-AD-005/006 | `[N]` | `anchor wrap`/manual fallback、idempotent install、backup、dry-run、drift | 无 hook 时可手工恢复；安装幂等/可回滚；不覆盖用户配置 | AD-01 |
| TODO-AD-03 | REQ-AD-007/008 | `[P]` | 静态 D/unverified contract 已有；补版本化 capability descriptor、真实/recorded/fake 标签 | 每能力有 D/V/S 和版本；UI/测试不把 mock 当 real | AD-01 |
| TODO-AD-04 | REQ-AD-009/010/011/012/013 | `[N]` | argv/绝对路径/最小 env/timeout、MCP 注册、context 限制、drift、fail-open/policy 边界 | 无 shell 注入；超时不冒充阻断；MCP 非必经代理；上下文不超限 | AD-01；SEC |
| TODO-AD-05 | REQ-CFG-001..007 | `[N]` | 统一 config/rules/MCP/skills 源头、managed blocks、plan/diff、secret refs、rollback、规则触发 Anchor 工具 | 一源多渲染；保留用户内容；drift/rollback；真实 Claude 规则触发 | AD-01/02；VT |
| TODO-AD-06 | REQ-CFG-008 | `[N]` | 三适配器 skills 安装/激活语义、幂等 managed files | unsupported activation 不声称；按 adapter 实测 | AD-01/05 |
| TODO-AD-07 | REQ-AD-013 | `[N]` | capture fail-open、policy enforcement=best_effort_hook/wrapper/advisory/observed/unavailable | 不承诺超过可执行边界；UI/doctor 显示真实状态 | AD-01/02；SEC |

## 8. causal-memory bridge

| ID | 需求 | 状态 | 剩余工作 | 验收标准 | 依赖 |
|---|---|---|---|---|---|
| TODO-CM-01 | REQ-CM-001/002/010/011 | `[N]` | 远程 MCP 客户端、HTTPS/token/超时/大小/schema、tools/list 发现、只存引用 | 无本地 DB/`causal.db`；官方 MCP 不重造；token 不落日志/上下文 | O-005；安全 |
| TODO-CM-02 | REQ-CM-003/004/005/013 | `[N]` | 启动搜索/注入 lessons、完成 propose、证据/用户确认、跨设备 causal_write_id | proposed≠confirmed；无证据/确认不结论；跨设备幂等 | CM-01；ST/EVID |
| TODO-CM-03 | REQ-CM-006/007/008/009/012 | `[N]` | 重试/jitter、离线队列、circuit breaker、stale cache、untrusted output、retention/delete | 网络失败核心不阻塞；降级可见；输出不执行；队列可删除 | CM-01；UI/doctor |
| TODO-CM-04 | causal 关联 UI/查询 | `[N]` | 树节点显示 causal 引用/教训；不把任务完成当因果 | 只读引用可追溯；confirmed 需证据/用户 | CM-02；UI |

## 9. Git sync / cross-device

| ID | 需求 | 状态 | 剩余工作 | 验收标准 | 依赖 |
|---|---|---|---|---|---|
| TODO-SY-01 | REQ-SY-001/003/004/009 | `[N]` | 事件/config/manifests Git 同步；事件 union；tombstone；私有 state repo 默认不自动建 | 无 `causal.db`；delete/tombstone 存活；无 auto remote | O-001 默认；WS |
| TODO-SY-02 | REQ-SY-002/006/010/011 | `[N]` | 分歧检测、merge/rebase 策略、no force-push、ownership fencing、checksums/signed optional | 不丢事件；不 force-push；凭据/秘密不入 remote/log | Git 安全；CONC |
| TODO-SY-03 | REQ-SY-005/007/008/012 | `[N]` | artifact bundle/patch 设计、离线 queue、device metadata、byte transport 关闭默认 | 缺字节 incomplete；无自动上传；离线可重试 | O-007/O-014；RST |
| TODO-SY-04 | REQ-WS-005 / sync projections | `[N]` | `TREE.md`/`state.json` 默认本地不同步；同步面只含权威事件/config/manifests | 无绝对路径/设备私有状态同步 | WS-03/04；SY-01 |

## 10. Vault / secrets

| ID | 需求 | 状态 | 剩余工作 | 验收标准 | 依赖 |
|---|---|---|---|---|---|
| TODO-VT-01 | REQ-VT-001/002/003/005/006/007/008 | `[N]` | OS keychain 三平台、refs、runtime 注入、rotation/revocation、scope、stdin/fd/0600、audit 无值 | 秘密不出上下文/日志/同步/argv；keychain 真机测试；audit 无值 | O-006；NFR 平台 |
| TODO-VT-02 | REQ-VT-004/009/010 | `[P]` | 现有窄正则 secret scan 覆盖 capture/evidence/export；补统一 redaction、doctor/CI 扫描、fake-only 策略 | fake key 可识别；真实 key 不进入测试/产物；redaction 全路径 | SEC-003 |
| TODO-VT-03 | secret-free UI/export/sync | `[N]` | Web/MCP/Git/log 全路径 redaction | 泄漏 corpus 全拒绝；错误不回显 | VT-01/02 |

## 11. CLI / Web UI

| ID | 需求 | 状态 | 剩余工作 | 验收标准 | 依赖 |
|---|---|---|---|---|---|
| TODO-UI-01 | REQ-UI-001 | `[P]` | 现有 init/event/node/note/evidence/ownership takeover/handoff/status/doctor/adapter；缺 branch/claim/release/capture 真实/restore/sync/config/vault/memory/web/install/uninstall/migrate | 文档命令全存在；JSON/exit 稳定；无危险默认 | 各域实现 |
| TODO-UI-02 | REQ-UI-002/003 | `[P]` | 已实现命令 JSON/exit 部分一致；补全 contract/exit/JSON tests、TTY/色彩/可操作性 | CLI contract 通过；机器输出稳定 | UI-01 |
| TODO-UI-03 | REQ-UI-004..007 | `[N]` | tree/evidence/causal/ownership/sync/restore 视图、loopback/token/CSRF/Origin、无秘密、read-only、degraded | Playwright/安全/a11y 通过；断网/无服务降级 | UI-01；SEC-007 |
| TODO-UI-04 | REQ-NFR-007 | `[N]` | 键盘/非色彩状态/可访问性 | a11y smoke 通过 | UI-03 |

## 12. Ownership / evidence / concurrency

| ID | 需求 | 状态 | 剩余工作 | 验收标准 | 依赖 |
|---|---|---|---|---|---|
| TODO-CONC-01 | REQ-CONC-001/002/004 | `[P]` | 有 claim/takeover/fencing；缺 heartbeat、lease expiry、stale takeover audit、完整 owner UI；不得自动最高 token 赢 | 同节点单 owner；过期/接管审计；旧 token 写拒绝；owner 可见 | WS/CLI |
| TODO-CONC-02 | REQ-CONC-003/005 | `[V]` | revision/expected_revision、确定性 DAG、并发隔离已验证 | 并发冲突明确；无 ULID/clock 权威 | 无 |
| TODO-EVID-01 | REQ-EVID-001 | `[P]` | 现有 id/node/kind/sha256/size/schema；缺 command/exit/actor/timestamp/verification state | 证据字段完整可追溯；不可变 | 数据模型 |
| TODO-EVID-02 | REQ-EVID-002/005/006 | `[V]` | content-address、done 证据、跨节点拒绝、不可变、override fail-closed | 无字节不得 done；证据绑节点；摘要非证据 | 无 |
| TODO-EVID-03 | REQ-EVID-003/004 | `[N]` | successor 验证事件、causal link 证据/确认门禁 | 继任者显式验证；任务完成不自动因果 | RST/CM |

## 13. Security / NFR / Roost / Ops

| ID | 需求 | 状态 | 剩余工作 | 验收标准 | 依赖 |
|---|---|---|---|---|---|
| TODO-SEC-01 | REQ-SEC-001/002/003/004 | `[P]` | 路径/普通文件/O_NOFOLLOW/秘密扫描/无 shell 部分已有；补 openat2 级防护、父目录 symlink、Windows、hook argv/timeout、MCP/sync/web 输入校验 | 全部输入源有统一验证器；TOCTOU/边界测试通过；不执行捕获内容 | 各域 |
| TODO-SEC-02 | REQ-SEC-005/006/007 | `[N]` | MCP HTTPS/host allowlist/timeout/size/schema；Git 无 force/凭据；Web loopback/CSRF/Origin | 无秘密日志；恶意远端/Origin 测试通过 | CM/SY/UI |
| TODO-NFR-01 | REQ-NFR-001 | `[E]` | Linux 本机通过；Windows/macOS build/e2e/keychain/install 未验收 | 三平台 release-blocking 矩阵通过 | CI/runner |
| TODO-NFR-02 | REQ-NFR-002 | `[N]` | hook p95<150ms、restore<1s、UI<2s 预算测量 | 报告环境/预算；不得伪造 | 真实 hooks |
| TODO-NFR-03 | REQ-NFR-003/004 | `[P]` | 核心本地/无遥测部分成立；远端降级/日志redaction未完整 | 离线核心可用；无 telemetry；日志本地 redacted | 各域 |
| TODO-NFR-04 | REQ-NFR-005/006/007 | `[N]` | retention/export/delete 未实现；无 unsafe/确定性模块仅局部；补 UX/a11y | 数据可删/导出；Go 无 unsafe；a11y | UI/VT |
| TODO-RT-01 | REQ-RT-001/002/003 | `[P]` | 文档边界存在；无 runtime-provider interface；未改 Roost 仓；core 无 Roost 依赖 | 可选 interface、feature flag、无 Roost 硬依赖；不修改三仓 | 产品决策 O-010 |
| TODO-OPS-01 | REQ-OPS-001/006/007 | `[N]` | checksums/signatures/SBOM/依赖锁/可复现构建；无真实支付/秘密约束已遵守 | 发布包可校验；无真实支付/密钥 | CI/打包 |
| TODO-OPS-02 | REQ-OPS-002/003 | `[N]` | upgrade/migrate/rollback/uninstall，数据保留/purge | dry-run/备份/回滚/保留数据通过 | WS-04/WS-06 |
| TODO-OPS-03 | REQ-OPS-004 | `[N]` | CI format/lint/unit/integration/contract/e2e/security/coverage、real gate | CI 可复现；mock 不替代 real | 测试/runner |
| TODO-OPS-04 | REQ-OPS-005/008 | `[P]` | 设计文档已有；缺用户/迁移/uninstall/known-limitations/覆盖更新；README/docs06/matrix 状态 stale | 文档与实际一致；限制非空；Roost 边界文档 | DOC-01 |
| TODO-DOC-01 | 状态文档同步 | `[N]` | 更新 README、`docs/06` §9、`acceptance/matrix.yaml`、本 TODO 的 actual status；保留 planned 标签 | 不再声称“无代码/全 planned”；TODO 与报告一致 | 本文件 |

## 14. Deliverables / release gate

| ID | 交付物 | 状态 | 剩余工作/验收 |
|---|---|---|---|
| TODO-DEL-01 | 可安装 CLI/二进制与三平台包 | `[N]` | checksum/signature、install/uninstall、Windows/macOS e2e |
| TODO-DEL-02 | 真实 Claude Code adapter + Codex/Gemini contract/real | `[N]` | 真实 e2e、capability version、真实/recorded/fake 标签 |
| TODO-DEL-03 | 完整 handoff/restore + code apply/verify | `[P]` | 有 hashes/可选字节；缺 apply、successor verification、跨设备 |
| TODO-DEL-04 | causal-memory bridge | `[N]` | 远程 MCP、证据/确认门禁、离线降级 |
| TODO-DEL-05 | Git sync | `[N]` | union/tombstone/conflict/no-force/ownership |
| TODO-DEL-06 | config/rules/skills/vault | `[N]` | managed blocks、keychain、secret refs |
| TODO-DEL-07 | Web UI/CLI full surface | `[P]` | 视图/安全/a11y/完整命令 |
| TODO-DEL-08 | CI/coverage/known-limitations/migration docs | `[N]` | 发布矩阵和文档；不把 planned 当 actual |
| TODO-DEL-09 | 完整产品独立验收 | `[N]` | 不是当前 35a2a46 的模块验收；需全 release gate |

## 15. 明确未交付 / 不能宣称

- 不是完整产品，不是完整跨 Agent、跨设备恢复，不是 native hook 验证通过。
- 本地 export/import 始终 `incomplete` / `code_apply=disabled` / `adapter_native_recovery=unverified`；不恢复事件日志/workspace，不 apply Git commit，不执行包内命令。
- 三适配器仍是 `D / unverified / e2e=false`；render fragment 是 documentation-only，不可视为可安装。
- 没有 local causal engine / `causal.db` / Git causal DB；也没有 causal-memory bridge 实现。
- 没有 MCP server/proxy；没有 sync/config/vault/web/install/migrate/uninstall。
- 没有 Windows/macOS e2e、没有真实 keychain、没有真实 Agent e2e、没有性能预算结论。
- Secret scanner 是窄正则；路径防护非 openat2；旧 schema 无自动迁移。
- `acceptance/matrix.yaml` 仍是 planned baseline；其 `planned` 状态不能当作实际通过。

## 16. 依赖/开放决策

| ID | 决策/依赖 | 影响 |
|---|---|---|
| O-003 | 支持的 Agent CLI 版本 | AD/CAP/E2E |
| O-005 | causal-memory MCP endpoint / 测试账号策略 | CM |
| O-006 | Linux 无 Secret Service 的 vault fallback | VT |
| O-007 | uncommitted code artifact：bundle/patch/both | RST/SY |
| O-008 | transcript 注入深度 | RST/CAP |
| O-009 | Web UI 是否 LAN | UI/SEC |
| O-010 | Roost boundary concrete contract | RT |
| O-002/O-020 | Windows/macOS/Windows runner | NFR/OPS/DEL |
| 用户未确认项 | 不得自行把提案写成已确认需求；O-001/O-014 仅有保守默认 | 设计/实现 |

## 17. 建议下一步顺序（不删功能）

1. `TODO-ST-*` + `TODO-WS-03/04`：补 Session Tree 模型、provenance、constraints、projection/migration。
2. `TODO-RST-04/02` + `TODO-EVID-03`：code apply/backup/rollback、successor verification、context injection。
3. `TODO-AD-01/04`：Claude Code 真实 adapter 全链路；Codex/Gemini contract 后真实 e2e。
4. `TODO-CM-*`：远程 MCP bridge。
5. `TODO-SY-*`：Git sync。
6. `TODO-AD-05/06` + `TODO-VT-*`：config/rules/skills/vault。
7. `TODO-UI-*`：完整 CLI + Web UI。
8. `TODO-OPS-*`、`TODO-NFR-*`、`TODO-DOC-01`：CI、打包、迁移/uninstall、三平台、文档、最终独立验收。
