# Known failures

These are known test failures with scheduled follow-up work. They are not release notes or product limitations.

| Test | Where it stopped | Observed result | Blocks held | Status |
| --- | --- | --- | --- | --- |
| Packaged chat acceptance | `settings-pages` journey — reopen Chat from Settings | Timed out reopening Chat from Settings. | 183 | moved — item 2vm |
| Packaged chat acceptance | `worker-plans` journey — worker plan result | Timed out waiting for a worker plan result. | 184, 185 | moved — item 2vm |
| Packaged chat acceptance | `connections` journey — Settings connection Test control | Timed out finding the Settings connection Test control. | 184 | moved — item 2vm |
| Packaged chat acceptance | `attachments-and-ocr` journey — retained SVG journal event | Timed out waiting for the retained SVG journal event. | 185 | moved — item 2vm |
| Packaged chat acceptance setup | disposable candidate signing | Untimestamped test signing completed with timestamp networking blocked. | 183, 185 | closed — item 2vm |
| `scheduled-work` journey | silent scheduled job chat cleanup | The silent job left a Scheduled chat on both isolated attempts. | 187 | open — item 2vp |
| `skills` journey | replacement skill source | The source still contained the old acceptance skill on both isolated attempts. | 187 | open — item 2vp |
| `chat-management` journey | bulk delete while a chat is running | Timed out waiting for bulk delete to name the running chat on both isolated attempts. | 187 | open — item 2vp |
| `compaction` journey | `context.compacted` event | Twelve short exchanges produced no `context.compacted` event on either isolated attempt. | 187 | open — item 2vp |
| `Profiles and Connections share fields, whole numbers, switches, and stable failure layout 2t0` | Playwright settings layout | Passed one of three isolated reruns, fewer than the required two. | 186 | open — item 2vp |
| `Settings geometry follows the six-color console contract 2sy` | Playwright 1920 px geometry | Failed all three isolated reruns at 1920 px. | 187 | open — item 2vp |
| `Chats shows only profile reading approval context and delete 2sb` | Playwright colour measurement | Ten consecutive isolated runs passed after waiting for the stable current menu state. | 185 | closed — item 2vn |
| `Chats shows only profile reading approval context and delete 2sb` | Playwright menu overlay | Ten consecutive isolated runs passed after waiting for the stable current menu state. | 185 | closed — item 2vn |
