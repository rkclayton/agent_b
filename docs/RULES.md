# Rules

This page lists every rule that can end a run, refuse a call, or change a result.
A rule that is not on this page does not exist; the build enforces the list, and adding a row requires the operator's word named in that row.
Read each row as its exact trigger, visible words, setting, and authority.
This page describes the build of 2026-10-16.

## 1 THE RUN ENDS

| Rule | When | You see | Set where | Asked by |
| --- | --- | --- | --- | --- |
| **Your approval** |  |  |  |  |
| Cancel during model | Stop is pressed while the model is answering. | `aborted_mid_model` | always | built-in |
| Cancel during run | Stop is pressed between a model answer and a call. | `aborted_mid_run` | always | built-in |
| Cancel during tool | Stop is pressed while a tool is running. | `aborted_mid_tool` | always | built-in |
| Cancellation requested | The active run receives its cancellation signal. | `cancellation_requested` | always | built-in |
| User stop | The user stops the active run. | `user_stop` | always | built-in |
| **Your folders and files** |  |  |  |  |
| Tool failures | The run ends after tool failures prevent useful progress. | `tool_errors` | always | built-in |
| **The model's context** |  |  |  |  |
| Context ceiling | The next model request cannot fit the usable context. | `context_ceiling` | connection context | built-in |
| Context exhausted | The server reports that its context is exhausted. | `context_exhausted` | connection context | built-in |
| Cycle detected | One tool's loop key gives the same result again: three times tells the model once; six asks you, or ends the run when nobody is there. | `cycle` | always | operator 2026-10-23 |
| Finished normally | The model completes the task. | `done` | always | built-in |
| Length reached | The model response reaches its output length. | `length` | connection context | built-in |
| Announced, no call | The model twice in a row announces an action and makes no call. | `announced_action_and_stopped` | always | operator 2026-10-19 |
| Malformed turn | The model twice names tool calls without returning calls. | `malformed_turn` | always | built-in |
| Model error | The model server returns an ordinary model error. | `model_error` | connection | built-in |
| Model unreachable | The configured model server cannot be reached. | `model_unreachable` | connection | built-in |
| Reasoning-only reply | The model returns reasoning but no final reply. | `reply_empty_reasoning_shown` | always | built-in |
| Reply empty | The model returns no usable reply. | `reply_empty` | always | built-in |
| **The web** |  |  |  |  |
| **Memory** |  |  |  |  |
| **Scheduling** |  |  |  |  |
| Connection unavailable | A scheduled or queued run has no runnable connection. | `connection_not_runnable` | connection | built-in |
| Mailbox stop | The local mailbox asks the active run to stop. | `mailbox_stop` | operator files | built-in |
| Schedule timeout | A scheduled run reaches its configured duration. | `cron_timeout` | Settings → schedules → run time | built-in |
| Wall-clock limit | A run reaches its configured wall-clock duration. | `wall_clock` | run time limit | built-in |

## 2 A CALL IS REFUSED

| Rule | When | You see | Set where | Asked by |
| --- | --- | --- | --- | --- |
| **Your approval** |  |  |  |  |
| Elevated application start | Agent_b starts with an elevated Administrator token. | SECURITY: Agent_b refuses to run with an elevated Administrator token; local administrators can launch it normally by double-clicking start-Agent_b.cmd in File Explorer (do not use Run as administrator) | always | item 2as |
| User override unavailable | A tool has no implementation for running as the signed-in user. | `error: tool %s has no user-identity override` | always | built-in |
| **Your folders and files** |  |  |  |  |
| Project choice required | A repository note could belong to more than one active project. | `error: repository note refused: name one project: ` | always | built-in |
| Project missing | A repository note has no active project. | `error: repository note refused: no project is in scope` | always | built-in |
| Tool unavailable | The requested tool is absent or disabled for this agent. | `error: tool %s is not available` | Settings → profiles → tools | built-in |
| **The model's context** |  |  |  |  |
| Call error | A tool returns an error to the model. | `error: ` | always | built-in |
| **The web** |  |  |  |  |
| **Memory** |  |  |  |  |
| Scope invalid | A saved note names a scope outside the offered choices. | `error: scope %q is not one of user, repository or environment; use planner for local product feedback` | always | operator 2026-10-08 |
| Scope required | A saved note omits its scope. | `error: scope is required: user, repository, environment, or planner` | always | operator 2026-10-08 |
| **Scheduling** |  |  |  |  |

## 3 A RESULT IS CHANGED

| Rule | When | You see | Set where | Asked by |
| --- | --- | --- | --- | --- |
| **Your approval** |  |  |  |  |
| **Your folders and files** |  |  |  |  |
| **The model's context** |  |  |  |  |
| Result saved and cut | A tool result exceeds the remaining model context and its full text is saved. | `[tool result cut: %d bytes; full output saved at %s; read_file path=%s offset=%d to page the omitted middle]` | connection context | built-in |
| Result save failed | A tool result exceeds the remaining model context and saving its full text fails. | `[tool result cut: %d bytes; saving the full output failed: %v]` | connection context | built-in |
| Server byte cut | A non-windowed result exceeds one quarter of a server's learned request-byte limit. | `[tool result cut to fit the server's %d-byte request limit]` | connection context | built-in |
| Results pruned at the line | At the compaction line, older tool results are at least half of the changeable context; each becomes one line, and the summary runs only if that is not enough. | `[%s result removed at compaction, %d tokens; recall reads it]` | connection context | operator 2026-10-22 |
| **The web** |  |  |  |  |
| Search engine benched | A search engine is temporarily skipped after its recorded failures or rate limit. | `engines_benched: %s` | Settings → tools → web search | operator 2026-09-28 |
| **Memory** |  |  |  |  |
| **Scheduling** |  |  |  |  |

## WHAT IS NOT A RULE

The per-result window pointer tells the model where the saved remainder is; it adds no stop or refusal.
The engine-benched header reports the dated engine state; it adds no restriction beyond the row above.
